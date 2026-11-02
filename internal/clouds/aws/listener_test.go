package aws

import (
	"testing"

	sdk "github.com/aws/aws-sdk-go-v2/aws"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
)

const testLB = "arn:aws:elasticloadbalancing:us-east-1:123456789012:loadbalancer/app/loomplane/50dc6c495c0c9188"

func TestEnsureListenerFindsAPlainForward(t *testing.T) {
	f := &fakeListeners{listeners: []elbtypes.Listener{
		plainForward(testOtherListener, 80, testOtherTG),
		plainForward(testListenerARN, 8000, testBaselineTG),
	}}
	l, err := ensureListener(t.Context(), f, testLB, testBaselineTG, "")
	if err != nil {
		t.Fatal(err)
	}
	if l.ARN != testListenerARN || l.Port != 8000 || l.HTTPS || len(f.created) != 0 {
		t.Fatalf("listener %+v, created %+v", l, f.created)
	}
}

func TestEnsureListenerFindsAWeightedForward(t *testing.T) {
	f := &fakeListeners{listeners: []elbtypes.Listener{
		plainForward(testOtherListener, 80, testOtherTG),
		plainForward(testListenerARN, 8000, testBaselineTG),
	}}
	// A canary splits the listener between the baseline and its candidate,
	// with the baseline at 0% while it promotes.
	if err := setListenerWeights(t.Context(), f, testListenerARN, []TargetWeight{{testBaselineTG, 0}, {testCandidateTG, 100}}); err != nil {
		t.Fatal(err)
	}
	l, err := ensureListener(t.Context(), f, testLB, testBaselineTG, "")
	if err != nil {
		t.Fatal(err)
	}
	if l.ARN != testListenerARN || l.Port != 8000 || len(f.created) != 0 {
		t.Fatalf("listener %+v, created %+v", l, f.created)
	}
}

func TestEnsureListenerOpensTheFirstFreePort(t *testing.T) {
	f := &fakeListeners{listeners: []elbtypes.Listener{
		plainForward(testOtherListener, 80, testOtherTG),
		plainForward(testListenerARN, 8000, testCandidateTG),
	}}
	l, err := ensureListener(t.Context(), f, testLB, testBaselineTG, "")
	if err != nil {
		t.Fatal(err)
	}
	if l.Port != 8001 || len(f.created) != 1 {
		t.Fatalf("listener %+v, created %+v", l, f.created)
	}
	a := f.created[0].DefaultActions[0]
	if sdk.ToString(f.created[0].LoadBalancerArn) != testLB || sdk.ToString(a.TargetGroupArn) != testBaselineTG {
		t.Fatalf("created %+v", f.created[0])
	}
}

func TestEnsureListenerServesHTTPSWithACertificate(t *testing.T) {
	f := &fakeListeners{}
	l, err := ensureListener(t.Context(), f, testLB, testBaselineTG, "arn:aws:acm:us-east-1:123456789012:certificate/abc")
	if err != nil {
		t.Fatal(err)
	}
	if l.Port != 443 || !l.HTTPS || f.created[0].Protocol != elbtypes.ProtocolEnumHttps || len(f.created[0].Certificates) != 1 {
		t.Fatalf("listener %+v, created %+v", l, f.created[0])
	}
}
