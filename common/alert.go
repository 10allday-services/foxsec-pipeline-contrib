package common

import (
	"fmt"
	"time"
)

const (
	ALERT_NEW          = "NEW"
	ALERT_ACKNOWLEDGED = "ACKLNOWLEDGED"
	ALERT_ESCALATED    = "ESCALATED"
)

type Alert struct {
	Id        string       `json:"id"`
	Severity  string       `json:"severity"`
	Category  string       `json:"category"`
	Summary   string       `json:"summary"`
	Payload   string       `json:"payload"`
	Metadata  []*AlertMeta `json:"metadata"`
	Timestamp time.Time    `json:"timestamp"`
}

func (a *Alert) PrettyPrint() string {
	var md string
	for _, am := range a.Metadata {
		md = md + fmt.Sprintf(" - %s=%s\n", am.Key, am.Value)
	}
	s := fmt.Sprintf(`Id: %s
Summary: %s
Severity: %s
Category: %s
Timestamp: %s
Payload: %s
Metadata:
%s`,
		a.Id, a.Summary, a.Severity, a.Category, a.Timestamp, a.Payload, md)
	return s
}

func (a *Alert) OlderThan(dur time.Duration) bool {
	return a.Timestamp.Add(dur).Before(time.Now())
}

func (a *Alert) IsStatus(s string) bool {
	for _, am := range a.Metadata {
		if am.Key == "status" {
			return am.Value == s
		}
	}
	return false
}

func (a *Alert) GetMetadata(key string) string {
	for _, am := range a.Metadata {
		if am.Key == key {
			return am.Value
		}
	}
	return ""
}

func (a *Alert) SetMetadata(key, value string) {
	for _, am := range a.Metadata {
		if am.Key == key {
			am.Value = value
			return
		}
	}
	a.Metadata = append(a.Metadata, &AlertMeta{Key: key, Value: value})
}

type AlertMeta struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}
