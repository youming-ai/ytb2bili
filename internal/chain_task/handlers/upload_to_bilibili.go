package handlers

import (
	"bili-up-backend/internal/chain_task/base"
	"bili-up-backend/internal/chain_task/manager"
	"bili-up-backend/internal/core"
	"bili-up-backend/internal/core/services"
	"bili-up-backend/internal/storage"
	"github.com/difyz9/bilibili-go-sdk/bilibili"
	"bili-up-backend/pkg/cos"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type UploadToBilibili struct {
	base.BaseTask
	App               *core.AppServer
	SavedVideoService *services.SavedVideoService
}

func NewUploadToBilibili(name string, app *core.AppServer, stateManager *manager.StateManager, client *cos.CosClient, savedVideoService *services.SavedVideoService) *UploadToBilibili {
	return &UploadToBilibili{
		BaseTask: base.BaseTask{
			Name:         name,
			StateManager: stateManager,
			Client:       client,
		},
		App:               app,
		SavedVideoService: savedVideoService,
	}
}

func (t *UploadToBilibili) Execute(context map[string]interface{}) bool {
	t.App.Logger.Info("========================================")
	t.App.Logger.Info("开始上传视频到 Bilibili")
	t.App.Logger.Info("========================================")

	// 1. 检查登录信息
	loginStore := storage.GetDefaultStore()
	if !loginStore.IsValid() {
		t.App.Logger.Error("❌ 没有有效的 Bilibili 登录信息，请先扫码登录")
		context["error"] = "未登录 Bilibili"
		return false
	}

	loginInfo, err := loginStore.Load()
	if err != nil {
		t.App.Logger.Errorf("❌ 加载登录信息失败: %v", err)
		context["error"] = fmt.Sprintf("加载登录信息失败: %v", err)
		return false
	}

	t.App.Logger.Infof("✓ 已加载登录信息，用户 MID: %d", loginInfo.TokenInfo.Mid)

	// 2. 查找下载的视频文件
	videoFiles := t.findVideoFiles()
	if len(videoFiles) == 0 {
		errMsg := "未找到视频文件"
		t.App.Logger.Error("❌ " + errMsg)
		context["error"] = errMsg
		return false
	}

	videoPath := videoFiles[0] // 使用第一个视频文件
	t.App.Logger.Infof("📹 找到视频文件: %s", filepath.Base(videoPath))

	// 3. 创建上传客户端
	uploadClient := bilibili.NewUploadClient(loginInfo)

	// 4. 上传视频文件到 Bilibili
	t.App.Logger.Info("⏫ 开始上传视频到 Bilibili...")
	video, err := uploadClient.UploadVideo(videoPath)
	if err != nil {
		userFriendlyError := t.getUserFriendlyError(err, "上传视频")
		t.App.Logger.Errorf("❌ 上传视频失败: %v", err)
		context["error"] = userFriendlyError
		return false
	}

	t.App.Logger.Infof("✓ 视频上传成功！")
	t.App.Logger.Infof("  Filename: %s", video.Filename)
	t.App.Logger.Infof("  Title: %s", video.Title)

	// 5. 准备投稿信息
	studio := t.buildStudioInfo(video, context)

	// 6. 提交视频到 Bilibili
	t.App.Logger.Info("📝 提交视频投稿信息...")
	result, err := uploadClient.SubmitVideo(studio)
	if err != nil {
		userFriendlyError := t.getUserFriendlyError(err, "提交视频")
		t.App.Logger.Errorf("❌ 提交视频失败: %v", err)
		context["error"] = userFriendlyError
		return false
	}

	// 7. 检查提交结果
	if result.Code != 0 {
		errMsg := fmt.Sprintf("提交失败: code=%d, message=%s", result.Code, result.Message)
		t.App.Logger.Error("❌ " + errMsg)
		context["error"] = errMsg
		return false
	}

	// 9. 保存上传结果到数据库
	context["bili_video"] = video
	context["bili_result"] = result

	// 10. 保存结果信息到数据库和context
	t.App.Logger.Info("💾 保存上传结果到数据库...")
	savedVideo, err := t.SavedVideoService.GetVideoByVideoID(t.StateManager.VideoID)
	if err != nil {
		t.App.Logger.Errorf("❌ 获取视频记录失败: %v", err)
	} else {
		// 尝试从 result.Data 中解析 BVID 和 AID
		if result.Data != nil {
			if dataMap, ok := result.Data.(map[string]interface{}); ok {
				if bvid, exists := dataMap["bvid"]; exists {
					if bvidStr, ok := bvid.(string); ok {
						savedVideo.BiliBVID = bvidStr
						// 保存BVID到context供后续字幕上传使用
						context["bili_bvid"] = bvidStr
						t.App.Logger.Infof("📺 BVID: %s", bvidStr)
					}
				}
				if aid, exists := dataMap["aid"]; exists {
					if aidFloat, ok := aid.(float64); ok {
						savedVideo.BiliAID = int64(aidFloat)
						// 保存AID到context
						context["bili_aid"] = int64(aidFloat)
						t.App.Logger.Infof("🆔 AID: %d", int64(aidFloat))
					}
				}
			}
		}

		if err := t.SavedVideoService.UpdateVideo(savedVideo); err != nil {
			t.App.Logger.Errorf("❌ 保存上传结果到数据库失败: %v", err)
		} else {
			t.App.Logger.Info("✅ 上传结果已保存到数据库")
		}
	}

	// 10. 输出成功信息
	t.App.Logger.Info("========================================")
	t.App.Logger.Infof("✓ 视频投稿成功！")
	if savedVideo != nil && savedVideo.BiliBVID != "" {
		t.App.Logger.Infof("  BVID: %s", savedVideo.BiliBVID)
		t.App.Logger.Infof("  访问链接: https://www.bilibili.com/video/%s", savedVideo.BiliBVID)
	}
	t.App.Logger.Info("========================================")

	return true
}

// findVideoFiles 查找下载目录中的视频文件
func (t *UploadToBilibili) findVideoFiles() []string {
	var videoFiles []string
	videoExtensions := []string{".mp4", ".flv", ".mkv", ".webm", ".avi", ".mov"}

	files, err := os.ReadDir(t.StateManager.CurrentDir)
	if err != nil {
		t.App.Logger.Errorf("读取目录失败: %v", err)
		return videoFiles
	}

	for _, file := range files {
		if file.IsDir() {
			continue
		}

		ext := strings.ToLower(filepath.Ext(file.Name()))
		for _, videoExt := range videoExtensions {
			if ext == videoExt {
				fullPath := filepath.Join(t.StateManager.CurrentDir, file.Name())
				videoFiles = append(videoFiles, fullPath)
				break
			}
		}
	}

	return videoFiles
}

// buildStudioInfo 构建投稿信息
func (t *UploadToBilibili) buildStudioInfo(video *bilibili.Video, context map[string]interface{}) *bilibili.Studio {
	// 默认值
	title := t.StateManager.VideoID
	desc := "自动上传的视频"
	tags := "视频"
	coverURL := "" // 封面URL

	// 从数据库查询视频的标题和描述信息
	savedVideo, err := t.SavedVideoService.GetVideoByVideoID(t.StateManager.VideoID)
	if err != nil {
		t.App.Logger.Warnf("⚠️ 无法从数据库获取视频信息: %v，将使用默认值", err)
	} else {
		// 优先使用AI生成的标题
		if savedVideo.GeneratedTitle != "" {
			title = savedVideo.GeneratedTitle
			t.App.Logger.Infof("✓ 使用数据库中AI生成的标题: %s", title)
		} else if savedVideo.Title != "" {
			title = savedVideo.Title
			t.App.Logger.Infof("✓ 使用数据库中的原始标题: %s", title)
		}

		// 优先使用AI生成的描述
		if savedVideo.GeneratedDesc != "" {
			desc = savedVideo.GeneratedDesc
			t.App.Logger.Infof("✓ 使用数据库中AI生成的描述")
		} else if savedVideo.Description != "" {
			desc = savedVideo.Description
			t.App.Logger.Infof("✓ 使用数据库中的原始描述")
		}

		// 使用AI生成的标签
		if savedVideo.GeneratedTags != "" {
			tags = savedVideo.GeneratedTags
			t.App.Logger.Infof("✓ 使用数据库中AI生成的标签: %s", tags)
		}
	}

	// 从 context 获取下载的封面图片并上传作为封面
	if coverImagePath, ok := context["cover_image_path"].(string); ok && coverImagePath != "" {
		t.App.Logger.Infof("📸 找到封面图片: %s", filepath.Base(coverImagePath))

		// 创建上传客户端并上传封面
		loginStore := storage.GetDefaultStore()
		loginInfo, err := loginStore.Load()
		if err == nil {
			uploadClient := bilibili.NewUploadClient(loginInfo)
			uploadedCoverURL, err := uploadClient.UploadCover(coverImagePath)
			if err != nil {
				t.App.Logger.Errorf("❌ 上传封面失败: %v", err)
			} else {
				coverURL = uploadedCoverURL
				t.App.Logger.Infof("✓ 封面上传成功: %s", coverURL)
			}
		}
	}

	// 检查是否有中文字幕
	zhSRTPath := filepath.Join(t.StateManager.CurrentDir, "zh.srt")
	hasZhSubtitle := false
	if _, err := os.Stat(zhSRTPath); err == nil {
		hasZhSubtitle = true
		t.App.Logger.Info("✓ 检测到中文字幕文件")
	}

	// 更新video对象的Title为翻译后的标题
	video.Title = title
	t.App.Logger.Infof("✓ 设置视频Title为: %s", title)

	studio := &bilibili.Studio{
		Copyright:     1,                          // 1=自制（从其他平台搬运也算自制）
		Title:         t.truncateTitle(title, 80), // B站标题最长80字符
		Desc:          desc,
		Tag:           tags,
		Tid:           122,      // 138=搞笑，可以根据需要修改
		Cover:         coverURL, // 使用上传的封面URL
		Dynamic:       "发布了新视频！",
		OpenSubtitle:  hasZhSubtitle, // 如果有中文字幕则开启
		Interactive:   0,
		Dolby:         0,
		LosslessMusic: 0,
		NoReprint:     1, // 禁止转载
		OpenElec:      0,
		Videos: []bilibili.Video{
			*video,
		},
	}

	t.App.Logger.Infof("📋 投稿信息:")
	t.App.Logger.Infof("  标题: %s", studio.Title)
	t.App.Logger.Infof("  简介: %s", t.truncateString(studio.Desc, 100))
	t.App.Logger.Infof("  标签: %s", studio.Tag)
	t.App.Logger.Infof("  分区: %d", studio.Tid)
	t.App.Logger.Infof("  封面: %s", studio.Cover)
	t.App.Logger.Infof("  字幕: %v", studio.OpenSubtitle)

	return studio
}

// truncateString 截断字符串用于日志显示
func (t *UploadToBilibili) truncateString(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}

// truncateTitle 截断标题到指定长度
func (t *UploadToBilibili) truncateTitle(title string, maxLen int) string {
	runes := []rune(title)
	if len(runes) <= maxLen {
		return title
	}
	return string(runes[:maxLen-3]) + "..."
}

// getUserFriendlyError 将技术错误转换为用户友好的错误信息
func (t *UploadToBilibili) getUserFriendlyError(err error, operation string) string {
	errorStr := err.Error()

	// 网络相关错误
	if strings.Contains(errorStr, "broken pipe") || strings.Contains(errorStr, "connection reset") {
		return fmt.Sprintf("%s失败：网络连接中断，请检查网络状态后重试", operation)
	}

	if strings.Contains(errorStr, "timeout") || strings.Contains(errorStr, "deadline exceeded") {
		return fmt.Sprintf("%s失败：网络超时，请稍后重试", operation)
	}

	if strings.Contains(errorStr, "connection refused") {
		return fmt.Sprintf("%s失败：无法连接到B站服务器，请检查网络连接", operation)
	}

	if strings.Contains(errorStr, "no such host") || strings.Contains(errorStr, "dns") {
		return fmt.Sprintf("%s失败：网络域名解析失败，请检查网络设置", operation)
	}

	// 文件相关错误
	if strings.Contains(errorStr, "no such file") || strings.Contains(errorStr, "file not found") {
		return fmt.Sprintf("%s失败：找不到视频文件，请确认文件已正确下载", operation)
	}

	if strings.Contains(errorStr, "permission denied") {
		return fmt.Sprintf("%s失败：文件访问权限不足", operation)
	}

	if strings.Contains(errorStr, "file too large") {
		return fmt.Sprintf("%s失败：文件过大，超出B站上传限制", operation)
	}

	// B站API相关错误
	if strings.Contains(errorStr, "401") || strings.Contains(errorStr, "unauthorized") {
		return fmt.Sprintf("%s失败：登录状态已过期，请重新登录", operation)
	}

	if strings.Contains(errorStr, "403") || strings.Contains(errorStr, "forbidden") {
		return fmt.Sprintf("%s失败：账号权限不足或被限制", operation)
	}

	if strings.Contains(errorStr, "429") || strings.Contains(errorStr, "rate limit") {
		return fmt.Sprintf("%s失败：操作频率过快，请稍后再试", operation)
	}

	if strings.Contains(errorStr, "500") || strings.Contains(errorStr, "internal server error") {
		return fmt.Sprintf("%s失败：B站服务器临时异常，请稍后重试", operation)
	}

	if strings.Contains(errorStr, "upload chunks") {
		return fmt.Sprintf("%s失败：视频分片上传中断，可能是网络不稳定导致，请重试", operation)
	}

	// 通用错误处理
	if strings.Contains(errorStr, "failed to") {
		return fmt.Sprintf("%s失败：操作执行失败，请稍后重试", operation)
	}

	// 如果是未知错误，返回简化的错误信息
	return fmt.Sprintf("%s失败：发生未知错误，请重试或联系技术支持", operation)
}
