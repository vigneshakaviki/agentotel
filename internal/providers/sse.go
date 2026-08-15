package providers

import "bytes"

// sseDataFrames splits a raw SSE body into the payloads of its "data: "
// lines, in order, skipping the "[DONE]" sentinel both providers send as
// their final frame. Frames are newline-delimited per the SSE spec; a
// truncated final frame (capture was cut off at maxCaptureBytes) is simply
// dropped rather than causing a parse error.
func sseDataFrames(body []byte) [][]byte {
	var frames [][]byte
	for _, line := range bytes.Split(body, []byte("\n")) {
		line = bytes.TrimRight(line, "\r")
		data, ok := bytes.CutPrefix(line, []byte("data: "))
		if !ok {
			data, ok = bytes.CutPrefix(line, []byte("data:"))
			if !ok {
				continue
			}
		}
		data = bytes.TrimSpace(data)
		if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
			continue
		}
		frames = append(frames, data)
	}
	return frames
}
