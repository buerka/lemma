// Package templates embeds the Markdown templates shipped with Lemma.
package templates

import (
	"embed"
	"fmt"
	"text/template"
)

//go:embed *.tmpl
var FS embed.FS

// Render parses the named template from the embedded filesystem and executes it.
func Render(name string, data any) ([]byte, error) {
	t, err := template.New(name).Option("missingkey=error").ParseFS(FS, name)
	if err != nil {
		return nil, fmt.Errorf("parse template %s: %w", name, err)
	}
	var buf bytesBuffer
	if err := t.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("render template %s: %w", name, err)
	}
	return buf.Bytes(), nil
}

// Raw returns an embedded file unchanged (used for runner scripts).
func Raw(name string) ([]byte, error) {
	b, err := FS.ReadFile(name)
	if err != nil {
		return nil, fmt.Errorf("read template %s: %w", name, err)
	}
	return b, nil
}

// bytesBuffer avoids importing bytes in every caller while keeping Render
// allocation-light.
type bytesBuffer struct {
	b []byte
}

func (b *bytesBuffer) Write(p []byte) (int, error) {
	b.b = append(b.b, p...)
	return len(p), nil
}

func (b *bytesBuffer) Bytes() []byte { return b.b }
