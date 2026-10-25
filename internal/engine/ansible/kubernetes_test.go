package ansible

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

func TestPodName(t *testing.T) {
	if n := podName("gaia-RUN_123-poc.web"); n != "gaia-run-123-poc-web" {
		t.Errorf("podName = %s", n)
	}
	if n := podName(strings.Repeat("a", 70) + "-x"); len(n) > 63 {
		t.Errorf("too long: %s", n)
	}
	k := Kubernetes{}
	if _, err := k.Image(context.Background(), "nexus/ee:1"); err == nil {
		t.Error("a tag without AllowTags")
	}
	if ref, err := k.Image(context.Background(), "nexus/ee@sha256:abc"); err != nil || ref != "nexus/ee@sha256:abc" {
		t.Errorf("digest = %s %v", ref, err)
	}
}

// TestKubernetesRun runs a job as a pod in a real cluster:
// GAIA_K8S_CONTEXT names the kubeconfig context, GAIA_K8S_NAMESPACE the
// namespace and GAIA_K8S_EE an execution environment the cluster has.
func TestKubernetesRun(t *testing.T) {
	kctx, ns, ee := os.Getenv("GAIA_K8S_CONTEXT"), os.Getenv("GAIA_K8S_NAMESPACE"), os.Getenv("GAIA_K8S_EE")
	if kctx == "" || ns == "" || ee == "" {
		t.Skip("GAIA_K8S_CONTEXT, GAIA_K8S_NAMESPACE and GAIA_K8S_EE not set")
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(clientcmd.NewDefaultClientConfigLoadingRules(),
		&clientcmd.ConfigOverrides{CurrentContext: kctx}).ClientConfig()
	if err != nil {
		t.Fatal(err)
	}
	c, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	k := Kubernetes{Client: c, Config: cfg, Namespace: ns, AllowTags: true, Started: 2 * time.Minute}
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	block, _ := ssh.MarshalPrivateKey(priv, "")
	key := string(pem.EncodeToMemory(block))
	j := Job{
		Name: "gaia-k8s-test-" + time.Now().Format("150405"), Image: ee, Playbook: "site.yml", SSHKey: key, Timeout: 3 * time.Minute,
		Secrets: []string{"hunter2"},
		Project: func(dir string) error {
			return os.WriteFile(filepath.Join(dir, "site.yml"), []byte(`- hosts: localhost
  connection: local
  gather_facts: false
  tasks:
    - name: Say it
      ansible.builtin.debug: { msg: "hello hunter2" }
    - name: Change it
      ansible.builtin.copy: { content: "x", dest: /runner/changed.txt }
`), 0o600)
		},
	}
	var events []Event
	res, err := k.Run(context.Background(), j, func(e Event) error { events = append(events, e); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if res.RC != 0 || res.Stats.OK["localhost"] != 2 || res.Stats.Changed["localhost"] != 1 {
		t.Errorf("result = %+v", res)
	}
	saw := false
	for _, e := range events {
		if strings.Contains(e.Stdout, "hunter2") {
			t.Errorf("a secret in an event: %s", e.Stdout)
		}
		saw = saw || e.Task == "Change it" && e.Changed
	}
	if !saw {
		t.Errorf("no changed task among %d events", len(events))
	}
	// The pod is gone.
	time.Sleep(2 * time.Second)
	if p, err := c.CoreV1().Pods(ns).Get(context.Background(), podName(j.Name), metav1.GetOptions{}); err == nil && p.DeletionTimestamp == nil {
		t.Errorf("pod left: %s", p.Status.Phase)
	}
}
