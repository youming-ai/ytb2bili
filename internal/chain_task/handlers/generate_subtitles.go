package handlers

import (
	"github.com/difyz9/ytb2bili/internal/chain_task/base"
	"github.com/difyz9/ytb2bili/internal/chain_task/manager"
	"github.com/difyz9/ytb2bili/internal/core"
	"github.com/difyz9/ytb2bili/internal/core/services"
	"github.com/difyz9/ytb2bili/pkg/cos"
	"github.com/difyz9/ytb2bili/pkg/store/model"
	"github.com/difyz9/ytb2bili/pkg/utils"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type GenerateSubtitles struct {
	base.BaseTask
	App               *core.AppServer
	SavedVideoService *services.SavedVideoService
}

func NewGenerateSubtitles(name string, app *core.AppServer, stateManager *manager.StateManager, client *cos.CosClient, savedVideoService *services.SavedVideoService) *GenerateSubtitles {
	return &GenerateSubtitles{
		BaseTask: base.BaseTask{
			Name:         name,
			StateManager: stateManager,
			Client:       client,
		},
		App:               app,
		SavedVideoService: savedVideoService,
	}
}

// formatTime 将秒数转换为 SRT 时间格式 (HH:MM:SS,mmm)
func (t *GenerateSubtitles) formatTime(seconds float64) string {
	hours := int(seconds / 3600)
	minutes := int((seconds - float64(hours*3600)) / 60)
	secs := int(seconds - float64(hours*3600) - float64(minutes*60))
	milliseconds := int((seconds - float64(int(seconds))) * 1000)

	return fmt.Sprintf("%02d:%02d:%02d,%03d", hours, minutes, secs, milliseconds)
}

// generateSRT 生成 SRT 格式字幕内容
func (t *GenerateSubtitles) generateSRT(subtitles []model.SavedVideoSubtitle) string {
	var srtContent strings.Builder

	for i, subtitle := range subtitles {
		// SRT 序号（从1开始）
		srtContent.WriteString(fmt.Sprintf("%d\n", i+1))

		// 时间轴
		startTime := t.formatTime(subtitle.Offset)
		endTime := t.formatTime(subtitle.Offset + subtitle.Duration)
		srtContent.WriteString(fmt.Sprintf("%s --> %s\n", startTime, endTime))

		// 字幕文本
		srtContent.WriteString(subtitle.Text)
		srtContent.WriteString("\n\n")
	}

	return srtContent.String()
}

func (t *GenerateSubtitles) Execute(context map[string]interface{}) bool {
	t.App.Logger.Info("========================================")
	t.App.Logger.Info("开始生成字幕文件")
	t.App.Logger.Info("========================================")

	// 1. 从数据库读取视频信息
	savedVideo, err := t.SavedVideoService.GetVideoByID(t.StateManager.Id)
	if err != nil {
		t.App.Logger.Errorf("❌ 查询视频信息失败: %v", err)
		context["error"] = err.Error()
		return false
	}

	if savedVideo == nil {
		errMsg := "视频信息不存在"
		t.App.Logger.Error("❌ " + errMsg)
		context["error"] = errMsg
		return false
	}

	// 2. 检查字幕数据是否存在
	if savedVideo.Subtitles == "" || savedVideo.Subtitles == "null" {
		t.App.Logger.Warn("⚠️  视频没有字幕数据，跳过字幕生成")
		return true // 没有字幕不算错误，继续执行后续任务
	}

	// 3. 解析字幕 JSON 数据
	var subtitles []model.SavedVideoSubtitle
	if err := json.Unmarshal([]byte(savedVideo.Subtitles), &subtitles); err != nil {
		t.App.Logger.Errorf("❌ 解析字幕数据失败: %v", err)
		context["error"] = fmt.Sprintf("解析字幕数据失败: %v", err)
		return false
	}

	if len(subtitles) == 0 {
		t.App.Logger.Warn("⚠️  字幕数据为空，跳过字幕生成")
		return true
	}

	t.App.Logger.Infof("📝 找到 %d 条字幕", len(subtitles))

	// 4. 生成 SRT 内容
	srtContent := t.generateSRT(subtitles)

	// 5. 确保输出目录存在
	if err := os.MkdirAll(t.StateManager.CurrentDir, 0755); err != nil {
		t.App.Logger.Errorf("❌ 创建字幕目录失败: %v", err)
		context["error"] = err.Error()
		return false
	}

	// 6. 生成字幕文件路径
	srtFileName := fmt.Sprintf("%s.srt", t.StateManager.VideoID)
	srtFilePath := filepath.Join(t.StateManager.CurrentDir, srtFileName)

	// 7. 写入 SRT 文件
	if err := os.WriteFile(srtFilePath, []byte(srtContent), 0644); err != nil {
		t.App.Logger.Errorf("❌ 写入字幕文件失败: %v", err)
		context["error"] = fmt.Sprintf("写入字幕文件失败: %v", err)
		return false
	}

	// 8. 验证文件是否创建成功
	if _, err := os.Stat(srtFilePath); os.IsNotExist(err) {
		errMsg := "字幕文件创建失败"
		t.App.Logger.Error("❌ " + errMsg)
		context["error"] = errMsg
		return false
	}

	enSrtFileName := fmt.Sprintf("%s.srt", "en")
	enSrtFilePath := filepath.Join(t.StateManager.CurrentDir, enSrtFileName)

	if err := utils.CopyFile(srtFilePath, enSrtFilePath); err != nil {
		t.App.Logger.Errorf("❌ 复制英文字幕文件失败: %v", err)
		context["error"] = fmt.Sprintf("复制英文字幕文件失败: %v", err)
	}

	// 9. 保存字幕文件路径到 context，供后续任务使用
	context["subtitle_file"] = srtFilePath
	context["subtitle_count"] = len(subtitles)

	// 10. 显示字幕预览（前3条）
	previewCount := 3
	if len(subtitles) < previewCount {
		previewCount = len(subtitles)
	}
	t.App.Logger.Info("📋 字幕预览（前3条）：")
	for i := 0; i < previewCount; i++ {
		sub := subtitles[i]
		t.App.Logger.Infof("  [%d] %.2fs-%.2fs: %s",
			i+1,
			sub.Offset,
			sub.Offset+sub.Duration,
			truncateString(sub.Text, 50))
	}

	t.App.Logger.Infof("✓ 字幕文件生成成功: %s", srtFilePath)
	t.App.Logger.Infof("✓ 共生成 %d 条字幕", len(subtitles))
	t.App.Logger.Info("========================================")

	return true
}

// truncateString 截断字符串，避免日志过长
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
