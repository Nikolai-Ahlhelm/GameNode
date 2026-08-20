package ftp

import (
	"context"
	"crypto/tls"
	"errors"
	"io/fs"
	"os"
	"strings"
	"time"

	ftpserver "github.com/fclairamb/ftpserverlib"
	"github.com/spf13/afero"

	gnfs "gamenode/internal/filesystem"
)

type driver Service

func (d *driver) GetSettings() (*ftpserver.Settings, error) {
	settings := &ftpserver.Settings{
		ListenAddr:             d.options.ListenAddr,
		PublicHost:             d.options.PublicHost,
		IdleTimeout:            15 * 60,
		ConnectionTimeout:      30,
		Banner:                 "GameNode FTP service",
		DisableActiveMode:      true,
		DisableSite:            true,
		DisableMFMT:            true,
		DisableLISTArgs:        true,
		PasvConnectionsCheck:   ftpserver.IPMatchRequired,
		ActiveConnectionsCheck: ftpserver.IPMatchRequired,
	}
	if d.options.PassivePortStart > 0 {
		settings.PassiveTransferPortRange = &ftpserver.PortRange{Start: d.options.PassivePortStart, End: d.options.PassivePortEnd}
	}
	if d.options.RequireTLS {
		settings.TLSRequired = ftpserver.MandatoryEncryption
	}
	return settings, nil
}

func (d *driver) ClientConnected(cc ftpserver.ClientContext) (string, error) {
	d.log.Info("FTP client connected", "module", "FTP", "remote_address", cc.RemoteAddr().String())
	return "GameNode FTP service", nil
}

func (d *driver) ClientDisconnected(cc ftpserver.ClientContext) {
	d.log.Info("FTP client disconnected", "module", "FTP", "remote_address", cc.RemoteAddr().String())
}

func (d *driver) AuthUser(cc ftpserver.ClientContext, username, password string) (ftpserver.ClientDriver, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	root, err := (*Service)(d).authenticate(ctx, username, password)
	if err != nil {
		d.log.Warn("FTP authentication failed", "module", "FTP", "remote_address", cc.RemoteAddr().String())
		return nil, ErrInvalidCredentials
	}
	d.log.Info("FTP authentication succeeded", "module", "FTP", "remote_address", cc.RemoteAddr().String())
	return &rootedFS{service: d.files, root: root}, nil
}

func (d *driver) GetTLSConfig() (*tls.Config, error) {
	if d.tls == nil {
		return nil, nil
	}
	return d.tls.Clone(), nil
}

// rootedFS implements afero.Fs using GameNode's authoritative filesystem
// sandbox for every path resolution. The adapter never joins an FTP path to a
// host path by itself.
type rootedFS struct {
	service *gnfs.Service
	root    string
}

func (f *rootedFS) Name() string { return "gamenode-server-root" }

func ftpRelative(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	// FTP paths are conventionally rooted with '/'. Strip only that protocol
	// root marker and leave dot segments intact for GameNode's central sandbox
	// to validate; cleaning first would turn /../../secret into /secret.
	return strings.TrimLeft(name, "/")
}

func (f *rootedFS) existing(name string) (string, error) {
	return f.service.ResolveServerPath(f.root, ftpRelative(name))
}

func (f *rootedFS) mutation(name string) (string, error) {
	return f.service.ResolveServerMutationPath(f.root, ftpRelative(name))
}

func (f *rootedFS) Create(name string) (afero.File, error) {
	return f.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
}

func (f *rootedFS) Mkdir(name string, perm os.FileMode) error {
	return f.service.CreateDirectory(f.root, ftpRelative(name))
}

func (f *rootedFS) MkdirAll(name string, perm os.FileMode) error {
	relative := ftpRelative(name)
	if relative == "" || relative == "." {
		return nil
	}
	current := ""
	for _, segment := range strings.Split(relative, "/") {
		if current == "" {
			current = segment
		} else {
			current += "/" + segment
		}
		if _, err := f.service.ResolveServerPath(f.root, current); err == nil {
			continue
		} else if !errors.Is(err, gnfs.ErrNotFound) {
			return err
		}
		if err := f.service.CreateDirectory(f.root, current); err != nil && !errors.Is(err, gnfs.ErrAlreadyExists) {
			return err
		}
	}
	return nil
}

func (f *rootedFS) Open(name string) (afero.File, error) {
	target, err := f.existing(name)
	if err != nil {
		return nil, err
	}
	return os.Open(target)
}

func (f *rootedFS) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	write := flag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE|os.O_TRUNC|os.O_APPEND) != 0
	if !write {
		return f.Open(name)
	}
	target, err := f.existing(name)
	if err != nil {
		if !errors.Is(err, gnfs.ErrNotFound) || flag&os.O_CREATE == 0 {
			return nil, err
		}
		target, err = f.mutation(name)
		if err != nil {
			return nil, err
		}
	} else {
		info, statErr := os.Stat(target)
		if statErr != nil {
			return nil, statErr
		}
		if !info.Mode().IsRegular() {
			return nil, gnfs.ErrSpecialFile
		}
	}
	return os.OpenFile(target, flag, perm.Perm())
}

func (f *rootedFS) Remove(name string) error {
	target, err := f.existing(name)
	if err != nil {
		return err
	}
	info, err := os.Stat(target)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return gnfs.ErrExpectedFile
	}
	return f.service.Delete(f.root, ftpRelative(name), false)
}

func (f *rootedFS) RemoveDir(name string) error {
	target, err := f.existing(name)
	if err != nil {
		return err
	}
	info, err := os.Stat(target)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return gnfs.ErrExpectedDir
	}
	return f.service.Delete(f.root, ftpRelative(name), false)
}

func (f *rootedFS) RemoveAll(name string) error { return fs.ErrPermission }

func (f *rootedFS) Rename(oldname, newname string) error {
	return f.service.Move(f.root, ftpRelative(oldname), ftpRelative(newname))
}

func (f *rootedFS) Stat(name string) (os.FileInfo, error) {
	target, err := f.existing(name)
	if err != nil {
		return nil, err
	}
	return os.Stat(target)
}

func (f *rootedFS) Chmod(name string, mode os.FileMode) error {
	target, err := f.existing(name)
	if err != nil {
		return err
	}
	return os.Chmod(target, mode.Perm())
}

func (f *rootedFS) Chown(name string, uid, gid int) error { return fs.ErrPermission }

func (f *rootedFS) Chtimes(name string, atime, mtime time.Time) error {
	target, err := f.existing(name)
	if err != nil {
		return err
	}
	return os.Chtimes(target, atime, mtime)
}
