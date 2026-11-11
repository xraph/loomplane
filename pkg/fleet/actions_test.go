package fleet

import "testing"

func TestActionRequiresExactPayloadAndPins(t *testing.T) {
	valid := ActionInput{Kind: "suspend", IdempotencyKey: "suspend-synthetic", ExpectedRevision: 2, ExpectedGeneration: 1, Suspend: &SuspendInput{}}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*ActionInput){
		func(in *ActionInput) { in.ExpectedRevision = 0 },
		func(in *ActionInput) { in.ExpectedGeneration = 0 },
		func(in *ActionInput) { in.IdempotencyKey = "" },
		func(in *ActionInput) { in.Resume = &ResumeInput{} },
		func(in *ActionInput) { in.Kind = "retire" },
		func(in *ActionInput) { in.Suspend = nil },
	} {
		in := valid
		mutate(&in)
		if err := in.Validate(); err == nil {
			t.Fatalf("invalid action accepted: %+v", in)
		}
	}
}

func TestActionRestoreAndMoveRequireExplicitTargets(t *testing.T) {
	in := ActionInput{Kind: "restore", IdempotencyKey: "restore-synthetic", ExpectedRevision: 1, ExpectedGeneration: 1, Restore: &RestoreInput{RecoveryPointID: "point", TargetStageID: "stage", TargetProfileID: "small"}}
	if err := in.Validate(); err != nil {
		t.Fatal(err)
	}
	in.Restore.TargetStageID = ""
	if err := in.Validate(); err == nil {
		t.Fatal("restore without isolated target stage")
	}
	in.Restore = nil
	in.Kind = "move"
	in.Move = &MoveInput{TargetCloudID: "cloud", TargetRegion: "region", CutoverMode: "live"}
	if err := in.Validate(); err == nil {
		t.Fatal("unqualified live cutover accepted")
	}
	in.Move.CutoverMode = "offline"
	if err := in.Validate(); err != nil {
		t.Fatal(err)
	}
}
