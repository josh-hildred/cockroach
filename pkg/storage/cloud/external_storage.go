// Copyright 2020 The Cockroach Authors.
//
// Use of this software is governed by the Business Source License
// included in the file licenses/BSL.txt.
//
// As of the Change Date specified in that file, in accordance with
// the Business Source License, use of this software will be governed
// by the Apache License, Version 2.0, included in the file
// licenses/APL.txt.

package cloud

import (
	"context"
	"database/sql/driver"
	"io"

	"github.com/cockroachdb/cockroach/pkg/base"
	"github.com/cockroachdb/cockroach/pkg/roachpb"
	"github.com/cockroachdb/cockroach/pkg/security"
	"github.com/cockroachdb/cockroach/pkg/settings/cluster"
)

// This file is for interfaces only and should not contain any implementation
// code. All concrete implementations should be added to pkg/storage/cloudimpl.

// ExternalStorage provides an API to read and write files in some storage,
// namely various cloud storage providers, for example to store backups.
// Generally an implementation is instantiated pointing to some base path or
// prefix and then gets and puts files using the various methods to interact
// with individual files contained within that path or prefix. However,
// implementations must also allow callers to provide the full path to a given
// file as the "base" path, and then read or write it with the methods below by
// simply passing an empty filename. Implementations that use stdlib's
// `filepath.Join` to concatenate their base path with the provided filename
// will find its semantics well suited to this -- it elides empty components and
// does not append surplus slashes.
type ExternalStorage interface {
	io.Closer

	// Conf should return the serializable configuration required to reconstruct
	// this ExternalStorage implementation.
	Conf() roachpb.ExternalStorage

	// ExternalIOConf should return the configuration containing several server
	// configured options pertaining to an ExternalStorage implementation.
	ExternalIOConf() base.ExternalIODirConfig

	// Settings should return the cluster settings used to configure the
	// ExternalStorage implementation.
	Settings() *cluster.Settings

	// ReadFile is shorthand for ReadFileAt with offset 0.
	ReadFile(ctx context.Context, basename string) (io.ReadCloser, error)

	// ReadFileAt returns a Reader for requested name reading at offset.
	// ErrFileDoesNotExist is raised if `basename` cannot be located in storage.
	// This can be leveraged for an existence check.
	ReadFileAt(ctx context.Context, basename string, offset int64) (io.ReadCloser, int64, error)

	// WriteFile should write the content to requested name.
	WriteFile(ctx context.Context, basename string, content io.ReadSeeker) error

	// List enumerates files within the supplied prefix, calling the passed
	// function with the name of each file found, relative to the external storage
	// destination's configured prefix. If the passed function returns a non-nil
	// error, iteration is stopped it is returned. If delimiter is non-empty names
	// which have the same prefix, prior to the delimiter, are grouped into a
	// single result which is that prefix. The order that results are passed to
	// the callback is undefined.
	List(ctx context.Context, prefix, delimiter string, fn ListingFn) error

	// ListFiles returns files that match a globs-style pattern. The returned
	// results are usually relative to the base path, meaning an ExternalStorage
	// instance can be initialized with some base path, used to query for files,
	// then pass those results to its other methods.
	//
	// As a special-case, if the passed patternSuffix is empty, the base path used
	// to initialize the storage connection is treated as a pattern. In this case,
	// as the connection is not really reusable for interacting with other files
	// and there is no clear definition of what it would mean to be relative to
	// that, the results are fully-qualified absolute URIs. The base URI is *only*
	// allowed to contain globs-patterns when the explicit patternSuffix is "".
	ListFiles(ctx context.Context, patternSuffix string) ([]string, error)

	// Delete removes the named file from the store.
	Delete(ctx context.Context, basename string) error

	// Size returns the length of the named file in bytes.
	Size(ctx context.Context, basename string) (int64, error)

	// Writer returns a writer for the requested name.
	//
	// A Writer *must* be closed via either Close, and if closing returns a
	// non-nil error, that error should be handled or reported to the user -- an
	// implementation may buffer written data until Close and only then return
	// an error, or Write may retrun an opaque io.EOF with the underlying cause
	// returned by the subsequent Close().
	Writer(ctx context.Context, basename string) (io.WriteCloser, error)
}

// ListingFn describes functions passed to ExternalStorage.ListFiles.
type ListingFn func(string) error

// ExternalStorageFactory describes a factory function for ExternalStorage.
type ExternalStorageFactory func(ctx context.Context, dest roachpb.ExternalStorage) (ExternalStorage, error)

// ExternalStorageFromURIFactory describes a factory function for ExternalStorage given a URI.
type ExternalStorageFromURIFactory func(ctx context.Context, uri string,
	user security.SQLUsername) (ExternalStorage, error)

// SQLConnI encapsulates the interfaces which will be implemented by the network
// backed SQLConn which is used to interact with the userfile tables.
type SQLConnI interface {
	driver.QueryerContext
	driver.ExecerContext
}

// AccessIsWithExplicitAuth is used to check if the provided path has explicit
// authentication.
var AccessIsWithExplicitAuth func(path string) (bool, string, error)
