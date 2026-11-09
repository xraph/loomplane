package planning

import "testing"

func TestManagedClusterCrossProjectAttachment(t *testing.T) {
	for _, project := range []string{"", "other", "owner"} {
		p := &planner{in: Input{ProjectID: project, Connections: []ConnectionOption{{ID: "runtime", Cloud: "kubernetes", Ready: true, Details: map[string]string{"managed_attachment_id": "attachment", "managed_project_id": "owner"}}}}}
		result := p.targetConnection("worker", "target", "target", "kubernetes", "runtime")
		if (result != nil) != (project == "owner") {
			t.Fatalf("project %q crossed managed target boundary", project)
		}
	}
	static := ReviewedConnectionSettings("kubernetes", map[string]string{"namespace": "static"})
	if len(static) != 1 {
		t.Fatal("static placement changed")
	}
	managed := ReviewedConnectionSettings("kubernetes", map[string]string{"namespace": "runtime", "managed_attachment_id": "attachment", "managed_project_id": "owner", "managed_resource_id": "cluster"})
	if len(managed) != 4 {
		t.Fatal("managed identity not reviewed")
	}
}
