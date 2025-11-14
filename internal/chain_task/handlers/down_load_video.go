package handlers

import (
	"github.com/difyz9/ytb2bili/internal/chain_task/base"
	"github.com/difyz9/ytb2bili/internal/chain_task/manager"
	"github.com/difyz9/ytb2bili/internal/core"
	"github.com/difyz9/ytb2bili/pkg/cos"
	"github.com/difyz9/ytb2bili/pkg/utils"
	"bufio"
	"fmt"
	"gorm.io/gorm"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type DownloadVideo struct {
	base.BaseTask
	App *core.AppServer
	DB  *gorm.DB
}

func NewDownloadVideo(name string, app *core.AppServer, stateManager *manager.StateManager, client *cos.CosClient) *DownloadVideo {
	return &DownloadVideo{
		BaseTask: base.BaseTask{
			Name:         name,
			StateManager: stateManager,
			Client:       client,
		},
		App: app,
	}
}

// findYtDlp 查找系统中的 yt-dlp 可执行文件
func (t *DownloadVideo) findYtDlp() (string, error) {
	// 从配置中获取安装目录
	var installDir string
	if t.App.Config != nil && t.App.Config.YtDlpPath != "" {
		installDir = t.App.Config.YtDlpPath
	}

	// 创建 yt-dlp 管理器
	manager := utils.NewYtDlpManager(t.App.Logger, installDir)

	// 检查是否已安装
	if manager.IsInstalled() {
		path := manager.GetBinaryPath()
		t.App.Logger.Debugf("找到 yt-dlp: %s", path)
		return path, nil
	}

	return "", fmt.Errorf("未找到 yt-dlp，请确保已正确安装")
}

// getVideoURL 根据 VideoID 构建完整的视频 URL
func (t *DownloadVideo) getVideoURL() string {
	videoID := t.StateManager.VideoID

	// 如果已经是完整 URL，直接返回
	if strings.HasPrefix(videoID, "http://") || strings.HasPrefix(videoID, "https://") {
		return videoID
	}

	// YouTube 短 ID 格式
	if len(videoID) == 11 && !strings.Contains(videoID, "/") {
		return fmt.Sprintf("https://www.youtube.com/watch?v=%s", videoID)
	}

	// Bilibili BV 号
	if strings.HasPrefix(videoID, "BV") {
		return fmt.Sprintf("https://www.bilibili.com/video/%s", videoID)
	}

	// 默认作为 YouTube ID 处理
	return fmt.Sprintf("https://www.youtube.com/watch?v=%s", videoID)
}

func (t *DownloadVideo) Execute(context map[string]interface{}) bool {
	t.App.Logger.Info("========================================")
	t.App.Logger.Infof("开始下载视频: %s", t.StateManager.VideoID)
	t.App.Logger.Info("========================================")

	// 1. 查找 yt-dlp 可执行文件
	ytdlpPath, err := t.findYtDlp()
	if err != nil {
		t.App.Logger.Errorf("❌ %v", err)
		context["error"] = err.Error()
		return false
	}

	// 2. 确保下载目录存在
	if err := os.MkdirAll(t.StateManager.CurrentDir, 0755); err != nil {
		t.App.Logger.Errorf("❌ 创建下载目录失败: %v", err)
		context["error"] = err.Error()
		return false
	}

	// 3. 构建下载命令
	videoURL := t.getVideoURL()

	command := []string{
		ytdlpPath,
		"-P", t.StateManager.CurrentDir,
		"-o", "%(id)s.%(ext)s",
		"--cookies-from-browser", "chrome",
		"--merge-output-format", "mp4",
	}

	// 4. 添加代理配置（如果启用）
	if t.App.Config != nil && t.App.Config.ProxyConfig != nil && t.App.Config.ProxyConfig.UseProxy && t.App.Config.ProxyConfig.ProxyHost != "" {
		command = append(command, "--proxy", t.App.Config.ProxyConfig.ProxyHost)
		t.App.Logger.Infof("📡 使用代理: %s", t.App.Config.ProxyConfig.ProxyHost)
	}

	// 5. 添加视频标识符和URL
	command = append(command, "--", t.StateManager.VideoID)
	command = append(command, videoURL)

	t.App.Logger.Infof("执行命令: %s", strings.Join(command, " "))
	t.App.Logger.Infof("下载目录: %s", t.StateManager.CurrentDir)
	t.App.Logger.Infof("视频URL: %s", videoURL)

	// 6. 创建命令并设置输出管道
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Dir = t.StateManager.CurrentDir

	// 捕获标准输出和标准错误
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.App.Logger.Errorf("❌ 创建标准输出管道失败: %v", err)
		context["error"] = err.Error()
		return false
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.App.Logger.Errorf("❌ 创建标准错误管道失败: %v", err)
		context["error"] = err.Error()
		return false
	}

	// 7. 启动命令
	if err := cmd.Start(); err != nil {
		t.App.Logger.Errorf("❌ 启动下载命令失败: %v", err)
		context["error"] = err.Error()
		return false
	}

	// 8. 实时读取输出
	go t.logOutput(stdout, "INFO")
	go t.logOutput(stderr, "ERROR")

	// 9. 等待命令完成
	if err := cmd.Wait(); err != nil {
		t.App.Logger.Errorf("❌ 视频下载失败: %v", err)
		context["error"] = fmt.Sprintf("下载失败: %v", err)
		return false
	}

	// 10. 验证下载的文件
	downloadedFile := t.findDownloadedFile()
	if downloadedFile == "" {
		errMsg := "下载完成但未找到视频文件"
		t.App.Logger.Error("❌ " + errMsg)
		context["error"] = errMsg
		return false
	}

	// 11. 保存文件信息到 context
	context["downloaded_file"] = downloadedFile
	t.App.Logger.Infof("✓ 视频下载成功: %s", downloadedFile)
	t.App.Logger.Info("========================================")

	return true
}

// logOutput 实时输出日志
func (t *DownloadVideo) logOutput(reader io.Reader, level string) {
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		// 解析进度信息
		if strings.Contains(line, "[download]") {
			if strings.Contains(line, "Destination:") {
				t.App.Logger.Infof("📥 %s", line)
			} else if strings.Contains(line, "%") {
				// 进度信息，使用 Debug 级别避免日志过多
				t.App.Logger.Debugf("⏳ %s", line)
			} else {
				t.App.Logger.Infof("📥 %s", line)
			}
		} else if strings.Contains(line, "[ffmpeg]") {
			t.App.Logger.Infof("🔄 %s", line)
		} else {
			if level == "ERROR" {
				t.App.Logger.Warnf("⚠️  %s", line)
			} else {
				t.App.Logger.Debugf("%s", line)
			}
		}
	}
}

// findDownloadedFile 查找下载的视频文件
func (t *DownloadVideo) findDownloadedFile() string {
	// 查找目录下的 mp4 文件
	files, err := filepath.Glob(filepath.Join(t.StateManager.CurrentDir, "*.mp4"))
	if err != nil || len(files) == 0 {
		// 尝试查找其他视频格式
		for _, ext := range []string{"*.webm", "*.mkv", "*.flv"} {
			files, err = filepath.Glob(filepath.Join(t.StateManager.CurrentDir, ext))
			if err == nil && len(files) > 0 {
				break
			}
		}
	}

	if len(files) > 0 {
		// 返回最新的文件
		latestFile := files[0]
		latestTime := int64(0)

		for _, file := range files {
			info, err := os.Stat(file)
			if err != nil {
				continue
			}
			if info.ModTime().Unix() > latestTime {
				latestTime = info.ModTime().Unix()
				latestFile = file
			}
		}

		return latestFile
	}

	return ""
}
