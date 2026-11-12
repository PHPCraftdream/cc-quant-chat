//go:build windows

package local

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// dpapiStore: A as a DPAPI blob of the current user (CryptProtectData), in a file. The blob opens only
// for this user on this machine: a copy of the file elsewhere is worthless.
type dpapiStore struct{ path string }

func newDPAPIStore(path string) secretStore { return &dpapiStore{path: path} }

var dpapiEntropy = []byte("ccqc/client-secret/v1")

func blobOf(b []byte) *windows.DataBlob {
	if len(b) == 0 {
		return &windows.DataBlob{}
	}
	return &windows.DataBlob{Size: uint32(len(b)), Data: &b[0]}
}

func takeBlob(o *windows.DataBlob) []byte {
	out := append([]byte(nil), unsafe.Slice(o.Data, o.Size)...)
	_, _ = windows.LocalFree(windows.Handle(unsafe.Pointer(o.Data)))
	return out
}

func (s *dpapiStore) put(a []byte) error {
	var out windows.DataBlob
	name, _ := windows.UTF16PtrFromString("ccqc")
	if err := windows.CryptProtectData(blobOf(a), name, blobOf(dpapiEntropy), 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return err
	}
	return writeFileAtomic(s.path, []byte(encodeSecret(takeBlob(&out))+"\n"))
}

func (s *dpapiStore) get() ([]byte, error) {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrSecretNotFound
	}
	if err != nil {
		return nil, err
	}
	blob, err := decodeBlob(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, err
	}
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(blobOf(blob), nil, blobOf(dpapiEntropy), 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, errors.New("the DPAPI blob of the client secret does not open for this user: " + err.Error())
	}
	a := takeBlob(&out)
	if len(a) != 32 {
		return nil, errors.New("the stored client secret is damaged")
	}
	return a, nil
}
