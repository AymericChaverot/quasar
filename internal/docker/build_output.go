package docker

import (
	"encoding/json"
	"errors"
	"io"
)

// drainBuildOutput consumes the JSON stream returned by ImageBuild and
// surfaces the daemon's error message if the build failed.
func drainBuildOutput(r io.Reader) error {
	dec := json.NewDecoder(r)
	for {
		var msg struct {
			Error string `json:"error"`
		}
		if err := dec.Decode(&msg); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if msg.Error != "" {
			return errors.New(msg.Error)
		}
	}
}
