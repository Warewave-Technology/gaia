package settings

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/Warewave-Technology/gaia/internal/audit"
	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/repo"
)

// Split takes the settings kinds' documents out of catalog files, each
// as a settings file (catalog.SettingsPath). Patches of generated
// objects become files of their own.
func Split(files []catalog.File) (map[string][]byte, error) {
	out := map[string][]byte{}
	for _, f := range files {
		ext := f.Path[strings.LastIndex(f.Path, ".")+1:]
		if ext != "yaml" && ext != "yml" {
			continue
		}
		dec := yaml.NewDecoder(bytes.NewReader(f.Data))
		for {
			var doc yaml.Node
			err := dec.Decode(&doc)
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return nil, fmt.Errorf("%s: %w", f.Path, err)
			}
			var head struct {
				Kind     string `yaml:"kind"`
				Metadata struct {
					Name        string            `yaml:"name"`
					Annotations map[string]string `yaml:"annotations"`
				} `yaml:"metadata"`
			}
			if err := doc.Decode(&head); err != nil || !catalog.IsSetting(head.Kind) || head.Metadata.Name == "" {
				continue
			}
			patch := head.Metadata.Annotations[catalog.AnnotationMode] == "patch"
			p := catalog.SettingsPath(head.Kind, head.Metadata.Name, patch)
			for i := 2; out[p] != nil; i++ {
				p = strings.TrimSuffix(catalog.SettingsPath(head.Kind, head.Metadata.Name, patch), ".yaml") + fmt.Sprintf("-%d.yaml", i)
			}
			data, err := yaml.Marshal(&doc)
			if err != nil {
				return nil, err
			}
			out[p] = data
		}
	}
	return out, nil
}

// ImportGit fills an empty settings store from Git (design 17.7): the
// settings kinds of the platform repository at its head become the first
// version, as they are. It does nothing once there is a version, and
// returns the version it wrote.
func ImportGit(ctx context.Context, s *Store, m *repo.Mirrors, platform repo.Rev) (int64, error) {
	if v, err := s.Latest(ctx); err != nil || v > 0 {
		return 0, err
	}
	// With no version yet, the snapshot reads the settings from Git.
	snap, err := m.Head(ctx, platform)
	if err != nil {
		return 0, err
	}
	files, err := m.PlatformFiles(ctx, snap)
	if err != nil {
		return 0, err
	}
	changes, err := Split(files)
	if err != nil {
		return 0, err
	}
	if len(changes) == 0 {
		return 0, nil
	}
	var as []catalog.File
	for p, data := range changes {
		as = append(as, catalog.File{Path: p, Data: data})
	}
	if _, err := m.GraphWithSettings(ctx, snap, as); err != nil {
		return 0, fmt.Errorf("the settings in Git do not load on their own: %w", err)
	}
	reason := fmt.Sprintf("Git'ten içeri alındı: %s@%s", platform.Name, snap.Platform.Commit)
	d, err := s.Write(ctx, Write{Changes: changes, Author: "gaia", Reason: reason,
		Audit: audit.Entry{Action: "settings.import", Detail: map[string]string{"commit": snap.Platform.Commit}}})
	return d.Version, err
}
