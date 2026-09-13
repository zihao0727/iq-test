package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

const maxResponseBytes = 2 * 1024 * 1024

func readResponse(resp *http.Response) (responseBody, error) {
	var result responseBody
	if !strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
		if err != nil || len(body) > maxResponseBytes {
			return result, errors.New("上游响应读取失败或超过 2 MB")
		}
		if json.Unmarshal(body, &result) != nil || result.Status != "completed" {
			return result, errors.New("Responses 响应无效或未完成")
		}
		return result, nil
	}

	// SSE repeats output in deltas and the final response; bound wire data separately.
	reader := &io.LimitedReader{R: resp.Body, N: 16 * maxResponseBytes}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 4*maxResponseBytes)
	var data, text strings.Builder
	dispatch := func() (bool, error) {
		if data.Len() == 0 {
			return false, nil
		}
		raw := strings.TrimSpace(data.String())
		data.Reset()
		if raw == "[DONE]" {
			return false, errors.New("上游流已结束，但未收到完成事件")
		}
		var event struct {
			Type     string       `json:"type"`
			Delta    string       `json:"delta"`
			Response responseBody `json:"response"`
		}
		if json.Unmarshal([]byte(raw), &event) != nil {
			return false, errors.New("上游流式事件格式无效")
		}
		switch event.Type {
		case "response.output_text.delta":
			if text.Len()+len(event.Delta) > maxResponseBytes {
				return false, errors.New("上游文本超过 2 MB")
			}
			text.WriteString(event.Delta)
		case "response.completed":
			if event.Response.Status != "completed" {
				return false, errors.New("Responses 响应未完成")
			}
			result = event.Response
			total := 0
			for _, output := range result.Output {
				for _, content := range output.Content {
					total += len(content.Text)
				}
			}
			if total > maxResponseBytes {
				return false, errors.New("上游文本超过 2 MB")
			}
			result.StreamText = text.String()
			return true, nil
		case "error", "response.failed", "response.incomplete":
			return false, errors.New("上游流式生成失败或未完成")
		}
		return false, nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if done, err := dispatch(); done || err != nil {
				return result, err
			}
		} else if strings.HasPrefix(line, "data:") {
			value := strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " ")
			if data.Len()+len(value)+1 > 4*maxResponseBytes {
				return result, errors.New("上游流式事件过大")
			}
			data.WriteString(value)
			data.WriteByte('\n')
		}
	}
	if scanner.Err() != nil || reader.N == 0 {
		return result, errors.New("上游流读取失败、超时或超过大小限制")
	}
	if done, err := dispatch(); done || err != nil {
		return result, err
	}
	return result, errors.New("上游流提前断开，未收到完成事件")
}
