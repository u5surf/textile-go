package wallet

import (
	"bytes"
	"encoding/json"
	cafe "github.com/textileio/textile-go/core/cafe"
	"github.com/textileio/textile-go/crypto"
	"github.com/textileio/textile-go/wallet/model"
	"github.com/textileio/textile-go/wallet/util"
	uio "gx/ipfs/Qmb8jW1F6ZVyYPW1epc2GFRipmd3S8tJ48pZKBVPzVqj9T/go-ipfs/unixfs/io"
	"os"
	"path/filepath"
	"strings"
)

// AddPhoto add a photo to the local ipfs node
func (w *Wallet) AddPhoto(path string) (*AddDataResult, error) {
	// get a key to encrypt with
	key, err := crypto.GenerateAESKey()
	if err != nil {
		return nil, err
	}

	// read file from disk
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	// decode image
	reader, format, size, err := util.DecodeImage(file)
	if err != nil {
		return nil, err
	}

	// make a thumbnail
	reader.Seek(0, 0)
	var thumbFormat util.Format
	if *format == util.GIF {
		thumbFormat = util.GIF
	} else {
		thumbFormat = util.JPEG
	}
	thumb, err := util.MakeThumbnail(reader, thumbFormat, model.ThumbnailWidth)
	if err != nil {
		return nil, err
	}

	// get some meta data
	username, _ := w.datastore.Profile().GetUsername() // ignore if not present (not signed in)
	mpk, err := w.GetPubKey()
	if err != nil {
		return nil, err
	}
	mpkb, err := mpk.Bytes()
	if err != nil {
		return nil, err
	}

	// path info
	fpath := file.Name()
	ext := strings.ToLower(filepath.Ext(fpath))

	// get metadata
	reader.Seek(0, 0)
	meta, err := util.MakeMetadata(reader, fpath, ext, *format, thumbFormat, size.X, size.Y, username, w.version)
	if err != nil {
		return nil, err
	}
	metab, err := json.Marshal(meta)
	if err != nil {
		return nil, err
	}

	// encrypt files
	reader.Seek(0, 0)
	photocipher, err := util.GetEncryptedReaderBytes(reader, key)
	if err != nil {
		return nil, err
	}
	thumbcipher, err := crypto.EncryptAES(thumb, key)
	if err != nil {
		return nil, err
	}
	metacipher, err := crypto.EncryptAES(metab, key)
	if err != nil {
		return nil, err
	}
	mpkcipher, err := crypto.EncryptAES(mpkb, key)
	if err != nil {
		return nil, err
	}

	// create a virtual directory for the photo
	dirb := uio.NewDirectory(w.ipfs.DAG)
	if err := util.AddFileToDirectory(w.ipfs, dirb, bytes.NewReader(photocipher), "photo"); err != nil {
		return nil, err
	}
	if err := util.AddFileToDirectory(w.ipfs, dirb, bytes.NewReader(thumbcipher), "thumb"); err != nil {
		return nil, err
	}
	if err := util.AddFileToDirectory(w.ipfs, dirb, bytes.NewReader(metacipher), "meta"); err != nil {
		return nil, err
	}
	if err := util.AddFileToDirectory(w.ipfs, dirb, bytes.NewReader(mpkcipher), "pk"); err != nil {
		return nil, err
	}

	// pin the directory
	dir, err := dirb.GetNode()
	if err != nil {
		return nil, err
	}
	if err := util.PinDirectory(w.ipfs, dir, []string{"photo"}); err != nil {
		return nil, err
	}
	result := &AddDataResult{Id: dir.Cid().Hash().B58String(), Key: string(key)}

	// make an archive for remote pinning by the OS
	result.Archive, err = cafe.NewArchive(result.Id, filepath.Join(w.repoPath, "tmp"))
	if err != nil {
		return nil, err
	}
	defer result.Archive.Close()

	// add files
	if err := result.Archive.AddFile(photocipher, "photo"); err != nil {
		return nil, err
	}
	if err := result.Archive.AddFile(thumbcipher, "thumb"); err != nil {
		return nil, err
	}
	if err := result.Archive.AddFile(metacipher, "meta"); err != nil {
		return nil, err
	}
	if err := result.Archive.AddFile(mpkcipher, "pk"); err != nil {
		return nil, err
	}

	// all done
	return result, nil
}