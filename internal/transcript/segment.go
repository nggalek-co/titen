package transcript

import (
	"crypto/sha256"
	"fmt"
)

type Segment struct {
	ID        string `json:"id"`
	MeetingID string `json:"meetingId"`
	Speaker   string `json:"speaker"`
	Text      string `json:"text"`
	StartMS   int64  `json:"startMs"`
	EndMS     int64  `json:"endMs"`
}

func (s Segment) Key() string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%s/%d/%d/%s/%s", s.MeetingID, s.StartMS, s.EndMS, s.Speaker, s.Text))))
}
