// Package remotearchive performs isolated archive operations on Unix servers with Python 3.
package remotearchive

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// Each extraction goes into a newly created directory; archive entries cannot
// overwrite existing user files. Links, devices and escaping paths are rejected.
const script = `import sys,os,tarfile,zipfile,gzip,tempfile,shutil,stat
mode,source=sys.argv[1:]
source=os.path.abspath(source)
parent=os.path.dirname(source)
name=os.path.basename(source)
if source==parent: raise ValueError('the filesystem root cannot be archived')
if mode=='compress':
    dest=source+'.tar.gz'
    fd,tmp=tempfile.mkstemp(prefix='.wails-archive-',dir=parent)
    os.close(fd)
    try:
        with tarfile.open(tmp,'w:gz') as archive:
            archive.add(source,arcname=name,recursive=True)
        os.link(tmp,dest)
        print(dest)
    finally:
        os.unlink(tmp)
else:
    def safe_name(value):
        if '\\' in value or '\x00' in value or value.startswith('/') or '..' in value.split('/'):
            raise ValueError('archive contains unsafe path: '+repr(value))
        value=os.path.normpath(value)
        if value in ('','.'): return None
        return value
    target=tempfile.mkdtemp(prefix=name+'.extracted-',dir=parent)
    try:
        if zipfile.is_zipfile(source):
            with zipfile.ZipFile(source) as archive:
                for member in archive.infolist():
                    relative=safe_name(member.filename)
                    bits=(member.external_attr>>16)&0xffff
                    if stat.S_ISLNK(bits) or (stat.S_IFMT(bits) and not (stat.S_ISREG(bits) or stat.S_ISDIR(bits))):
                        raise ValueError('links and special files are not supported')
                    if relative is None: continue
                    dest=os.path.join(target,relative)
                    if member.is_dir(): os.makedirs(dest,exist_ok=True)
                    else:
                        os.makedirs(os.path.dirname(dest),exist_ok=True)
                        with archive.open(member) as src,open(dest,'xb') as dst: shutil.copyfileobj(src,dst)
        elif tarfile.is_tarfile(source):
            with tarfile.open(source,'r:*') as archive:
                for member in archive:
                    relative=safe_name(member.name)
                    if not (member.isdir() or member.isfile()): raise ValueError('links and special files are not supported')
                    if relative is None: continue
                    dest=os.path.join(target,relative)
                    if member.isdir(): os.makedirs(dest,exist_ok=True)
                    else:
                        os.makedirs(os.path.dirname(dest),exist_ok=True)
                        with archive.extractfile(member) as src,open(dest,'xb') as dst: shutil.copyfileobj(src,dst)
                        os.chmod(dest,member.mode&0o777)
        elif name.lower().endswith('.gz'):
            output=name[:-3]
            if not output: raise ValueError('invalid gzip filename')
            with gzip.open(source,'rb') as src,open(os.path.join(target,output),'xb') as dst: shutil.copyfileobj(src,dst)
        else: raise ValueError('unsupported archive format')
        print(target)
    except BaseException:
        shutil.rmtree(target)
        raise
`

func Quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := 64*1024 - b.Len()
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		b.Buffer.Write(p)
	}
	return n, nil
}

func Run(ctx context.Context, client *ssh.Client, source string, compress bool) (string, error) {
	if strings.TrimSpace(source) == "" || strings.ContainsRune(source, 0) {
		return "", errors.New("无效远程路径")
	}
	session, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()
	var stdout, stderr limitedBuffer
	session.Stdout = &stdout
	session.Stderr = &stderr
	action := "extract"
	if compress {
		action = "compress"
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- session.Run("python3 -c " + Quote(script) + " " + Quote(action) + " " + Quote(source)) }()
	select {
	case <-ctx.Done():
		session.Close()
		return "", fmt.Errorf("压缩操作中断：%w", ctx.Err())
	case err := <-done:
		if err != nil {
			return "", fmt.Errorf("远程压缩操作失败（需要 Python 3）：%s: %w", strings.TrimSpace(stderr.String()), err)
		}
	}
	return strings.TrimSpace(stdout.String()), nil
}
