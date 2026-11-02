package agentops

import "time"

// QuestionTTL is how long a run waits for a person to answer one of its
// requests before cortex gives up on it.
const QuestionTTL = 24 * time.Hour

// requestOps are the requests to a person. Cortex pauses the run on each
// as an external tool; agentrequests asks the person and hands the answer
// back.
var requestOps = []string{"ask_person", "request_secret", "request_github_connection", "request_cloud_connection"}

func registerRequests(r *Registry) {
	for _, name := range requestOps {
		RegisterExternal(r, def(name), QuestionTTL)
	}
}
