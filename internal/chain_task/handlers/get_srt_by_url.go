package handlers

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"gorm.io/gorm"
	"html"
	"io/ioutil"
	"net/http"

	"os"
	"regexp"
	"strconv"
	"strings"
	"github.com/difyz9/ytb2bili/internal/chain_task/base"
	"github.com/difyz9/ytb2bili/internal/chain_task/manager"
	"github.com/difyz9/ytb2bili/internal/core"
	"github.com/difyz9/ytb2bili/pkg/cos"
)

// XMLText XML 字幕中的文本元素
type XMLText struct {
	XMLName  xml.Name `xml:"text"`
	Start    string   `xml:"start,attr"`
	Duration string   `xml:"dur,attr"`
	Content  string   `xml:",chardata"`
}

// XMLTranscript XML 字幕文档
type XMLTranscript struct {
	XMLName xml.Name  `xml:"transcript"`
	Texts   []XMLText `xml:"text"`
}

// TextInfo 字幕信息
type TextInfo struct {
	StartTime float64 `json:"start_time"`
	Duration  float64 `json:"duration"`
	Content   string  `json:"content"`
}

// TranscriptData 字幕数据
type TranscriptData struct {
	Transcript []TextInfo `json:"transcript"`
}

// Task03Handler 获取字幕任务
type Task03Handler struct {
	base.BaseTask
	App *core.AppServer
	DB  *gorm.DB
}

// NewGetSubtitlesTask 创建获取字幕任务
func NewTask03Handler(name string, app *core.AppServer, db *gorm.DB, stateManager *manager.StateManager, client *cos.CosClient) *Task03Handler {
	return &Task03Handler{
		BaseTask: base.BaseTask{
			Name:         name, // "GetSubtitles",
			StateManager: stateManager,
			Client:       client,
		},
		App: app,
		DB:  db,
	}
}

// Execute 执行任务
func (t *Task03Handler) Execute(context map[string]interface{}) bool {
	videoID := t.StateManager.VideoID

	// 获取字幕 URL
	srtURL, err := t.getVideoSrtURL(videoID)
	if err != nil {
		fmt.Printf("获取字幕 URL 失败: %v\n", err)
		return false
	}

	// 获取字幕内容
	transcript, err := t.getSrtFile(srtURL)
	if err != nil {
		fmt.Printf("获取字幕内容失败: %v\n", err)
		return false
	}

	// 保存字幕到文件
	//transcriptFile := filepath.Join(t.StateManager.CurrentDir, "transcript.json")
	data, err := json.MarshalIndent(transcript, "", "  ")
	if err != nil {
		fmt.Printf("序列化字幕数据失败: %v\n", err)
		return false
	}
	//print(transcriptFile)
	if err := os.WriteFile(t.StateManager.OriginalJSON, data, 0644); err != nil {
		fmt.Printf("保存字幕文件失败: %v\n", err)
		return false
	}

	// 将字幕数据添加到上下文
	context["transcript"] = transcript

	fmt.Println("字幕获取成功")
	return true
}

// getVideoSrtURL 获取视频字幕 URL
func (t *Task03Handler) getVideoSrtURL(videoID string) (string, error) {

	url := fmt.Sprintf("https://www.youtube.com/watch?v=%s", videoID)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36")

	client := &http.Client{}
	//
	//if t.App.Config.HttpProxy != "" {
	//
	//
	//	// 创建 Transport，设置代理
	//	transport := &http.Transport{
	//		Proxy: http.ProxyURL(proxyURL),
	//	}
	//
	//	// 创建 HTTP 客户端
	//	client = &http.Client{
	//		Transport: transport,
	//	}
	//}

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("请求失败，状态码: %d", resp.StatusCode)
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	pattern := regexp.MustCompile(`https://www.youtube.com/api/timedtext\?v=[^"]*`)
	matches := pattern.FindStringSubmatch(string(body))
	if len(matches) == 0 {
		return "", fmt.Errorf("未找到字幕 URL")
	}

	srtURL := matches[0]
	srtURL = regexp.MustCompile(`\\u0026`).ReplaceAllString(srtURL, "&")

	return srtURL, nil
}

// getSrtFile 获取字幕文件内容
func (t *Task03Handler) getSrtFile(url string) (*TranscriptData, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/58.0.3029.110 Safari/537.3")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("请求失败，状态码: %d", resp.StatusCode)
	}

	var transcript XMLTranscript
	if err := xml.NewDecoder(resp.Body).Decode(&transcript); err != nil {
		return nil, err
	}

	var textInfos []TextInfo
	for _, text := range transcript.Texts {
		startTime, err := strconv.ParseFloat(text.Start, 64)
		if err != nil {
			return nil, fmt.Errorf("无法解析起始时间: %v", err)
		}

		duration, err := strconv.ParseFloat(text.Duration, 64)
		if err != nil {
			return nil, fmt.Errorf("无法解析持续时间: %v", err)
		}

		// 处理特殊字符

		textInfos = append(textInfos, TextInfo{
			StartTime: startTime,
			Duration:  duration,
			Content:   strings.ReplaceAll(html.UnescapeString(text.Content), "\u00A0", " "),
		})
	}

	return &TranscriptData{Transcript: textInfos}, nil
}
