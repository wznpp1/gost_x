package h2cProxy

import (
	mdata "github.com/go-gost/core/metadata"
	mdutil "github.com/go-gost/core/metadata/util"
)

const (
	defaultBacklog = 128
)

type metadata struct {
	path    string
	backlog int
}

func (l *h2cProxyListener) parseMetadata(md mdata.Metadata) (err error) {
	const (
		path    = "path"
		backlog = "backlog"
	)

	l.md.backlog = mdutil.GetInt(md, backlog)
	if l.md.backlog <= 0 {
		l.md.backlog = defaultBacklog
	}

	l.md.path = mdutil.GetString(md, path)
	return
}
