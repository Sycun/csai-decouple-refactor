package app

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The pending interrupt's answer surface answers from HITLQueue, not from AgentHandler. Same
// shape as TestApprovalConfigRoutesAnswerFromThePolicy: a route re-hung on the agent, or a twin
// method added back to it, fails here rather than in review.

var pendingEndpoints = []struct{ path, method string }{
	{"/hitl/pending", "ListHITLPending"},
	{"/hitl/decision", "DecideHITLInterrupt"},
	{"/hitl/dismiss", "DismissHITLInterrupt"},
}

func TestPendingInterruptRoutesAnswerFromTheQueue(t *testing.T) {
	routes, err := os.ReadFile("routes_hitl.go")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(routes), "\n")
	for _, ep := range pendingEndpoints {
		found := false
		for _, line := range lines {
			if strings.Contains(line, `"`+ep.path+`"`) && strings.Contains(line, "hitlQueue."+ep.method) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s %s is not registered on the queue collaborator", ep.method, ep.path)
		}
	}
}

func TestPendingInterruptMethodsAreNotOnAgentHandler(t *testing.T) {
	src := handlerSources(t)
	for _, ep := range pendingEndpoints {
		if regexp.MustCompile(`func \([a-z]+ \*AgentHandler\) ` + ep.method + `\(`).MatchString(src) {
			t.Errorf("AgentHandler grew %s back - \"what is waiting on a human\" is HITLQueue's", ep.method)
		}
		// The collaborator has to still own it, otherwise "not on the agent" is just a deletion.
		if !regexp.MustCompile(`func \([a-z]+ \*HITLQueue\) ` + ep.method + `\(`).MatchString(src) {
			t.Errorf("HITLQueue lost %s", ep.method)
		}
	}
}
