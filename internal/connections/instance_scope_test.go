package connections

import "testing"

func TestInstanceConnectionRejectsGenericProjectScope(t *testing.T) {
	c := &Connection{Method: MethodManagedKubernetes, Details: map[string]string{"managed_attachment_id": "attachment", "managed_project_id": "project", "managed_instance_id": "instance", "managed_environment_id": "environment"}}
	if ManagedProjectAllowed(c, "project") {
		t.Fatal("generic project operation admitted an instance namespace")
	}
	delete(c.Details, "managed_instance_id")
	if ManagedProjectAllowed(c, "project") {
		t.Fatal("incomplete instance pin admitted")
	}
	delete(c.Details, "managed_environment_id")
	if !ManagedProjectAllowed(c, "project") {
		t.Fatal("ordinary project enrollment rejected")
	}
}
