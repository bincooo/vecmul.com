package vecmul

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/RomiChan/websocket"
	"github.com/bincooo/emit.io"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

var stops = []string{
	"stop",
	"end_turn",
	"finished",
}

const (
	GPT35          = "GPT-3.5"
	GPT4           = "GPT-4"
	GPT4o          = "GPT-4o"
	Claude3Sonnet  = "Claude3-Sonnet"
	Claude35Sonnet = "Claude3.5-Sonnet"
	Claude3Opus    = "Claude3-Opus"
	Gemini15flash  = "gemini-1.5-flash"
	Gemini15pro    = "gemini-1.5-pro"

	host      = "api.vecmul.com"
	userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/127.0.0.0 Safari/537.36 Edg/127.0.0.0"
)

type Chat struct {
	proxies string
	session *emit.Session

	model string
	token string
}

type Data struct {
	Role           string `json:"role"`
	Content        string `json:"content"`
	FinishedReason string `json:"finishedReason"`

	Msg   string `json:"message"`
	Error error  `json:"-"`
}

func New(proxies, model, token string) *Chat {
	switch model {
	case GPT35, GPT4, GPT4o, Claude3Sonnet, Claude35Sonnet, Claude3Opus, Gemini15flash, Gemini15pro:
	default:
		model = GPT35
	}

	return &Chat{
		proxies: proxies,
		model:   model,
		token:   token,
	}
}

func (c *Chat) Session(session *emit.Session) *Chat {
	c.session = session
	return c
}

func (c *Chat) Reply(ctx context.Context, message string, attach string) (ch chan Data, err error) {
	messageType := "MESSAGE"
	if attach != "" {
		// gemini model is not support image
		messageType = "FILE_MESSAGE"
	}

	conn, response, err := emit.SocketBuilder(c.session).
		Context(ctx).
		Proxies(c.proxies).
		URL("wss://"+host+"/ws").
		Query("token", url.PathEscape("Bearer "+c.token)).
		Header("Host", host).
		Header("Origin", "https://www.vecmul.com").
		Header("accept-language", "en-US,en;q=0.9").
		Header("user-agent", userAgent).
		DoS(http.StatusSwitchingProtocols)
	if err != nil {
		return
	}

	body := map[string]interface{}{
		"type":      "CHAT",
		"spaceName": "Free Space",
		"message": map[string]interface{}{
			"isAnonymous":     true,
			"rootMsgId":       uuid.NewString(),
			"public":          false,
			"model":           c.model,
			"order":           0,
			"role":            "user",
			"content":         message,
			"relatedLinkInfo": nil,
			"messageType":     messageType,
			"fileId":          nil,
			"fileKey":         attach,
			"language":        "en-US",
		},
	}

	dataBytes, err := json.Marshal(body)
	if err != nil {
		return
	}

	err = conn.WriteMessage(websocket.TextMessage, dataBytes)
	if err != nil {
		return
	}

	ch = make(chan Data)
	go resolve(ctx, ch, conn, response)
	return
}

// 支持url/base64
func (c *Chat) Upload(ctx context.Context, file, name string) (key string, err error) {
	mime, data, err := c.loadAttach(ctx, file)
	if err != nil {
		return
	}

	var buffer bytes.Buffer
	w := multipart.NewWriter(&buffer)
	fw, _ := w.CreateFormFile("file", name)
	_, err = io.Copy(fw, bytes.NewBuffer(data))
	if err != nil {
		return
	}

	err = w.WriteField("fileType", mime)
	if err != nil {
		return
	}

	err = w.WriteField("fileName", name)
	if err != nil {
		return
	}
	_ = w.Close()
	response, err := emit.ClientBuilder(c.session).
		Context(ctx).
		Proxies(c.proxies).
		POST("https://"+host+"/api/v1/chat/upload-file").
		Header("origin", "https://www.vecmul.com").
		Header("accept-language", "en-US,en;q=0.9").
		Header("content-type", w.FormDataContentType()).
		Header("authorization", "Bearer "+c.token).
		Header("user-agent", userAgent).
		Header("host", host).
		Bytes(buffer.Bytes()).
		DoC(emit.Status(http.StatusOK), emit.IsJSON)
	if err != nil {
		if emit.IsJSON(response) == nil {
			err = errors.Join(err, errors.New(emit.TextResponse(response)))
		}
		return
	}

	defer response.Body.Close()
	obj, err := emit.ToMap(response)
	if err != nil {
		return
	}

	if succeed, ok := obj["succeed"]; ok && succeed.(bool) {
		return obj["key"].(string), nil
	}

	err = errors.New("upload failed")
	return
}

func resolve(ctx context.Context, ch chan Data, conn *websocket.Conn, response *http.Response) {
	defer conn.Close()
	defer response.Body.Close()
	defer close(ch)

	type chunk struct {
		D Data   `json:"data"`
		M string `json:"rootMsgId"`
		T string `json:"type"`
	}

	// return true 结束
	Do := func() bool {
		_, p, err := conn.ReadMessage()
		if err != nil {
			ch <- Data{Error: err}
			return true
		}

		data := chunk{}
		err = json.Unmarshal(p, &data)
		if err != nil {
			logrus.Error(err)
			return false
		}

		if data.T == "ERROR" {
			ch <- Data{Error: errors.New(data.D.Msg)}
			return true
		}

		if data.T != "AI_STREAM_MESSAGE" {
			return false
		}

		if slices.Contains(stops, data.D.FinishedReason) {
			return true
		}

		ch <- data.D
		return false
	}

	for {
		select {
		case <-ctx.Done():
			logrus.Error(ctx.Err())
			return
		default:
			if Do() {
				return
			}
		}
	}
}

func (c *Chat) loadAttach(ctx context.Context, url string) (mime string, data []byte, err error) {
	// base64
	if strings.HasPrefix(url, "data:image/") {
		pos := strings.Index(url, ";")
		if pos == -1 {
			err = errors.New("invalid base64 format")
			return
		}

		mime = url[5:pos]
		url = url[pos+1:]

		if !strings.HasPrefix(url, "base64,") {
			err = errors.New("invalid base64 format")
			return
		}
		data, err = base64.StdEncoding.DecodeString(url[7:])
		return
	}

	if !strings.HasPrefix(url, "http") {
		err = errors.New("invalid url")
		return
	}

	// url
	response, err := emit.ClientBuilder(c.session).
		GET(url).
		Context(ctx).
		Proxies(c.proxies).
		DoS(http.StatusOK)
	if err != nil {
		return
	}

	defer response.Body.Close()
	mime = response.Header.Get("content-type")
	data, err = io.ReadAll(response.Body)
	return
}
