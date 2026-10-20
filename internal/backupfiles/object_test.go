package backupfiles

import (
	"strings"
	"testing"
)

func TestObjectValidateDoesNotNeedStorage(t *testing.T) {
	valid := Object{Key: "org/project/point/attempt.dump", Size: 100, SHA256: strings.Repeat("a", 64)}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Object){
		func(o *Object) { o.Key = "../foreign.dump" },
		func(o *Object) { o.Size = -1 },
		func(o *Object) { o.Size = maxBytes + 1 },
		func(o *Object) { o.SHA256 = "not-a-checksum" },
		func(o *Object) { o.DestinationID = "not-a-destination" },
	} {
		object := valid
		change(&object)
		if err := object.Validate(); err == nil {
			t.Fatalf("invalid captured object accepted: %+v", object)
		}
	}
}
