package ansible

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

// Kubernetes runs each job as a pod of its own (design 13.3): the
// execution environment's image, ansible-runner in it, the private data
// directory in memory. The directory, with the SSH key, goes to the pod
// over exec, never through the API's objects; the events come back as
// the pod's log. The pod is deleted when the job ends.
type Kubernetes struct {
	Client    kubernetes.Interface
	Config    *rest.Config
	Namespace string
	// AllowTags runs an image named by tag; without it, an execution
	// environment must be pinned by digest so that the run record names
	// exactly what ran.
	AllowTags bool
	// Started is how long a pod may take to start (pull, schedule);
	// five minutes when zero.
	Started time.Duration
}

// Image returns ref when it is pinned by digest.
func (k Kubernetes) Image(_ context.Context, ref string) (string, error) {
	if strings.Contains(ref, "@sha256:") || k.AllowTags {
		return ref, nil
	}
	return "", fmt.Errorf("%s: the Kubernetes executor runs execution environments pinned by digest (image@sha256:...)", ref)
}

// podName is a pod name from a job name: DNS-1123, at most 63 characters.
var notDNS = regexp.MustCompile(`[^a-z0-9-]+`)

func podName(job string) string {
	n := strings.Trim(notDNS.ReplaceAllString(strings.ToLower(job), "-"), "-")
	if len(n) > 63 {
		n = strings.TrimRight(n[:63], "-")
	}
	return n
}

// Run runs the job in a pod, calling on for every event in order.
func (k Kubernetes) Run(ctx context.Context, j Job, on func(Event) error) (Result, error) {
	dir, err := os.MkdirTemp("", "gaia-run-")
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err := writeDataDir(dir, j); err != nil {
		return Result{}, err
	}
	bundle, err := tarDir(dir)
	if err != nil {
		return Result{}, err
	}
	_ = os.RemoveAll(dir) // the key leaves the disk at once

	if j.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, j.Timeout)
		defer cancel()
	}
	pods := k.Client.CoreV1().Pods(k.Namespace)
	name := podName(j.Name)
	no := false
	limit := resource.MustParse("512Mi")
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"app.kubernetes.io/name": "gaia-ansible", "gaia.dev/job": name}},
		Spec: corev1.PodSpec{
			RestartPolicy:                corev1.RestartPolicyNever,
			AutomountServiceAccountToken: &no,
			EnableServiceLinks:           &no,
			Containers: []corev1.Container{{
				Name:  "runner",
				Image: j.Image,
				// ansible-runner starts once the data directory is in.
				Command:      []string{"sh", "-c", `while [ ! -e /runner/.gaia-ready ]; do sleep 1; done; exec ansible-runner run /runner -p "$GAIA_PLAYBOOK" -j`},
				Env:          []corev1.EnvVar{{Name: "GAIA_PLAYBOOK", Value: j.Playbook}},
				VolumeMounts: []corev1.VolumeMount{{Name: "runner", MountPath: "/runner"}},
				SecurityContext: &corev1.SecurityContext{
					AllowPrivilegeEscalation: &no,
					Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
				},
			}},
			Volumes: []corev1.Volume{{Name: "runner", VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{Medium: corev1.StorageMediumMemory, SizeLimit: &limit},
			}}},
		},
	}
	if _, err := pods.Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		return Result{}, fmt.Errorf("ansible pod: %w", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		grace := int64(10)
		_ = pods.Delete(ctx, name, metav1.DeleteOptions{GracePeriodSeconds: &grace})
	}()
	if err := k.waitRunning(ctx, name); err != nil {
		return Result{}, err
	}
	if err := k.exec(ctx, name, []string{"sh", "-c", "tar xf - -C /runner && touch /runner/.gaia-ready"}, bytes.NewReader(bundle)); err != nil {
		return Result{}, fmt.Errorf("ansible pod: copy the data directory: %w", err)
	}
	logs, err := pods.GetLogs(name, &corev1.PodLogOptions{Container: "runner", Follow: true}).Stream(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("ansible pod: logs: %w", err)
	}
	m := newMasker(append([]string{j.SSHKey}, j.Secrets...))
	res, readErr := read(logs, m, on)
	_ = logs.Close()
	if ctx.Err() != nil {
		return res, fmt.Errorf("ansible-runner: %w", ctx.Err())
	}
	if readErr != nil {
		return res, readErr
	}
	rc, err := k.exitCode(ctx, name)
	if err != nil {
		return res, err
	}
	res.RC = rc
	if res.Stats.OK == nil {
		return res, fmt.Errorf("ansible-runner exited %d without a play recap", res.RC)
	}
	return res, nil
}

// waitRunning waits for the pod's container to run; a pod that cannot
// start (no image, not schedulable) fails at once or after Started.
func (k Kubernetes) waitRunning(ctx context.Context, name string) error {
	limit := k.Started
	if limit == 0 {
		limit = 5 * time.Minute
	}
	deadline := time.Now().Add(limit)
	for {
		p, err := k.Client.CoreV1().Pods(k.Namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("ansible pod: %w", err)
		}
		for _, cs := range p.Status.ContainerStatuses {
			if cs.State.Running != nil {
				return nil
			}
			if w := cs.State.Waiting; w != nil && (w.Reason == "ErrImagePull" || w.Reason == "ImagePullBackOff" || w.Reason == "InvalidImageName") {
				return fmt.Errorf("ansible pod: %s: %s", w.Reason, w.Message)
			}
			if t := cs.State.Terminated; t != nil {
				return fmt.Errorf("ansible pod ended before it started: %s", t.Reason)
			}
		}
		if p.Status.Phase == corev1.PodFailed {
			return fmt.Errorf("ansible pod failed: %s", p.Status.Message)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("ansible pod did not start in %s (%s)", limit, p.Status.Phase)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func (k Kubernetes) exitCode(ctx context.Context, name string) (int, error) {
	for range 30 {
		p, err := k.Client.CoreV1().Pods(k.Namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return 0, err
		}
		for _, cs := range p.Status.ContainerStatuses {
			if t := cs.State.Terminated; t != nil {
				return int(t.ExitCode), nil
			}
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return 0, errors.New("ansible pod: no exit code")
}

func (k Kubernetes) exec(ctx context.Context, pod string, cmd []string, stdin io.Reader) error {
	req := k.Client.CoreV1().RESTClient().Post().Resource("pods").Namespace(k.Namespace).Name(pod).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{Container: "runner", Command: cmd, Stdin: true, Stdout: true, Stderr: true}, scheme.ParameterCodec)
	ex, err := remotecommand.NewSPDYExecutor(k.Config, "POST", req.URL())
	if err != nil {
		return err
	}
	var out bytes.Buffer
	if err := ex.StreamWithContext(ctx, remotecommand.StreamOptions{Stdin: stdin, Stdout: &out, Stderr: &out}); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(out.String()))
	}
	return nil
}

// tarDir packs a directory, its modes kept.
func tarDir(dir string) ([]byte, error) {
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == dir {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		fi, err := d.Info()
		if err != nil {
			return err
		}
		if !fi.Mode().IsRegular() && !fi.IsDir() {
			return nil // links in an exported project are not followed
		}
		h, err := tar.FileInfoHeader(fi, "")
		if err != nil {
			return err
		}
		h.Name, h.Uid, h.Gid, h.Uname, h.Gname = filepath.ToSlash(rel), 0, 0, "", ""
		if fi.IsDir() {
			h.Name += "/"
		}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if fi.IsDir() {
			return nil
		}
		f, err := os.Open(p) //nolint:gosec // our own temporary directory
		if err != nil {
			return err
		}
		_, err = io.Copy(tw, f)
		_ = f.Close()
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// NewKubernetes is the executor inside a cluster: the pod's own service
// account, in namespace.
func NewKubernetes(namespace string) (Kubernetes, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return Kubernetes{}, err
	}
	c, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return Kubernetes{}, err
	}
	return Kubernetes{Client: c, Config: cfg, Namespace: namespace}, nil
}
