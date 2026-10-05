package transfer

import (
	"context"
	"fmt"
	"io"

	"github.com/pkg/sftp"
)

// Keep standard 32 KiB SFTP packets, but overlap their round trips. A window
// bounds outstanding data and checkpoint rollback to 512 KiB per file worker.
// File workers remain capped at four; this is request pipelining within a file.
const uploadRequests = 16
const uploadWindow = uploadRequests * 32 * 1024

type uploadReader struct {
	ctx context.Context
	io.Reader
}

func (r uploadReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}

func (s *Service) copyUpload(j *job, target *sftp.File, source io.Reader, remote string) error {
	reader := uploadReader{ctx: j.ctx, Reader: source}
	for j.progress.Transferred < j.progress.Total {
		if err := j.ctx.Err(); err != nil {
			return err
		}
		want := min(int64(uploadWindow), j.progress.Total-j.progress.Transferred)
		var n int64
		var err error
		if want <= 32*1024 {
			// One packet needs no worker goroutines, especially for small files.
			buffer := make([]byte, int(want))
			var read int
			read, err = io.ReadFull(reader, buffer)
			if err == nil {
				var written int
				written, err = target.Write(buffer[:read])
				n = int64(written)
			}
		} else {
			n, err = target.ReadFromWithConcurrency(io.LimitReader(reader, want), uploadRequests)
		}
		// ReadFromWithConcurrency returns bytes READ, not a contiguous count of
		// acknowledged WRITEs. On any error retain the previous checkpoint;
		// uploadPath truncates the uncertain window before attempting it again.
		if err != nil {
			return err
		}
		if n != want {
			return fmt.Errorf("%w: expected %d upload bytes, read %d", io.ErrUnexpectedEOF, want, n)
		}
		j.progress.Transferred += n
		j.uploadOffsets[remote] = j.progress.Transferred
		s.report(j, false)
	}
	s.report(j, true)
	return j.ctx.Err()
}
