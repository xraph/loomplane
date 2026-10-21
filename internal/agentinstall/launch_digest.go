package agentinstall

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	"github.com/xraph/loomplane/internal/workloadidentity"
)

// DesiredConfigMap is the same public payload used by Deliver. Controllers save
// its digest before any provider write so recovery can verify the exact result.
func (p *KubernetesPlan) DesiredConfigMap(record workloadidentity.EnrollmentRecord, settings LaunchSettings) (*corev1.ConfigMap, error) {
	cfg, err := p.launch(record, settings, time.Now())
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(cfg)
	if err != nil || len(body) > 1<<20 {
		return nil, ErrLaunch
	}
	defer clear(body)
	cm := &corev1.ConfigMap{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"}, ObjectMeta: p.metadata(), Immutable: ptr.To(true), Data: map[string]string{ConfigKey: string(body)}}
	cm.OwnerReferences = []metav1.OwnerReference{{APIVersion: "v1", Kind: "Pod", Name: p.name, UID: types.UID(record.Binding.PodUID)}}
	return cm, nil
}

func ConfigMapDigest(cm *corev1.ConfigMap) (string, error) {
	if cm == nil || cm.Immutable == nil || !*cm.Immutable || len(cm.Data) != 1 || cm.Data[ConfigKey] == "" || len(cm.BinaryData) != 0 || len(cm.OwnerReferences) != 1 || cm.OwnerReferences[0].Kind != "Pod" || cm.OwnerReferences[0].APIVersion != "v1" || cm.OwnerReferences[0].UID == "" || cm.OwnerReferences[0].Name != cm.Name || cm.DeletionTimestamp != nil {
		return "", ErrLaunch
	}
	raw, err := json.Marshal(struct {
		Name, Namespace, Data string
		Owner                 metav1.OwnerReference
	}{cm.Name, cm.Namespace, cm.Data[ConfigKey], cm.OwnerReferences[0]})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
