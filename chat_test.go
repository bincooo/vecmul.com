package vecmul

import (
	"context"
	"encoding/base64"
	"github.com/bincooo/emit.io"
	"os"
	"testing"
)

var (
	session *emit.Session

	token = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiI2NTciLCJyb2xlIjoiUk9MRV9VU0VSIiwiZW1haWwiOiJiaW5nY28uem5AZ21haWwuY29tIiwiaXNWZXJpZmllZCI6dHJ1ZSwiaWF0IjoxNzIyMzI5NTg5LCJleHAiOjE3MjIzMzY3ODl9.W7KmZX_VZkZ9xCwLOo3Uv4qKs2LUWfCdyk3J4DrQqUM"
)

func init() {
	session, _ = emit.NewSession("http://127.0.0.1:7890", emit.SimpleWithes("127.0.0.1"))
}

func TestChat(t *testing.T) {
	chat := New("http://127.0.0.1:7890", GPT4o, token)
	chat.Session(session)

	dataBytes, err := os.ReadFile("blob.jpg")
	if err != nil {
		t.Fatal(err)
	}

	key, err := chat.Upload(context.Background(), "data:image/jpeg;base64,"+base64.StdEncoding.EncodeToString(dataBytes), "1.jpg")
	if err != nil {
		t.Fatal(err)
	}

	ch, err := chat.Reply(context.Background(), "图里有什么？", key)
	if err != nil {
		t.Fatal(err)
	}

	echo(t, ch)
}

func echo(t *testing.T, ch chan Data) {
	content := ""
	for d := range ch {
		if d.Error != nil {
			t.Fatal(d.Error)
		}

		t.Log(d.Content)
		content += d.Content
	}

	t.Log("------------------")
	t.Log(content)
}
