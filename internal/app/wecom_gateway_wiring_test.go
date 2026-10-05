package app

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The 企业微信 wire layer answers from WecomGateway, not from RobotHandler. Same reason for the
// same gate as the approval policy: without it, the split survives only until someone adds
// "one more method to the handler that already has 63".

var wecomWireMethods = []string{
	"wecomRequireToken", "HandleWecomGET", "signWecomRequest",
	"HandleWecomPOST", "sendWecomReply", "sendWecomMessageViaAPI",
}

func handlerPackageSources(t *testing.T) string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "handler", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	read := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		read++
		b.Write(data)
	}
	if read < 40 {
		t.Fatalf("the scan read %d handler files; it is reading a subset", read)
	}
	return b.String()
}

func TestWecomCallbacksAnswerFromTheGateway(t *testing.T) {
	routes, err := os.ReadFile("routes_robot.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(routes)
	for _, method := range []string{"HandleWecomGET", "HandleWecomPOST"} {
		wanted := "Wecom()." + method
		if !strings.Contains(text, wanted) {
			t.Errorf("/robot/wecom is not registered through the gateway (%s); the transport belongs on WecomGateway", wanted)
		}
	}
}

func TestWireProtocolMethodsAreNotOnRobotHandler(t *testing.T) {
	src := handlerPackageSources(t)
	var back []string
	for _, name := range wecomWireMethods {
		re := regexp.MustCompile(`func \([a-z] \*RobotHandler\) ` + regexp.QuoteMeta(name) + `\(`)
		if re.MatchString(src) {
			back = append(back, name)
		}
	}
	sort.Strings(back)
	if len(back) > 0 {
		t.Errorf("RobotHandler grew these wire-protocol methods back: %v. Signature and crypto concerns belong on WecomGateway.", back)
	}

	// The scan is only meaningful if the gateway still owns them: a deleted method would make
	// the first loop pass for the wrong reason.
	var missing []string
	for _, name := range wecomWireMethods {
		re := regexp.MustCompile(`func \([a-z] \*WecomGateway\) ` + regexp.QuoteMeta(name) + `\(`)
		if !re.MatchString(src) {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Errorf("WecomGateway lost %d of its %d wire methods: %v", len(missing), len(wecomWireMethods), missing)
	}
}
