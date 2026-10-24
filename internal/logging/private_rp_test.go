package logging

import (
	"strings"
	"testing"

	log "github.com/sirupsen/logrus"
)

func TestPrivateRPFormatterDropsBodiesAndFields(t *testing.T) {
	t.Setenv("CPA_PRIVATE_RP", "1")
	e := log.NewEntry(log.New())
	e.Message = "SECRET_BODY"
	e.Data["error"] = "SECRET_TOKEN"
	e.Data["request_id"] = "SECRET_ID"
	out, err := (&LogFormatter{}).Format(e)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "SECRET") {
		t.Fatal("sensitive output")
	}
}
