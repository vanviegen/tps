package daemon

import (
	"os"
	"strconv"
	"syscall"
	"unsafe"
)

// The size of the terminal a service writes to, which progress bars fit
// themselves to: about what the dashboard's console shows of a line.
const ptyRows, ptyCols = 40, 120

// openPty opens a pseudo-terminal for a service's output. Newlines go through
// as they are written (no output processing turns them into CRLF), so the log
// reads like one a file would have got.
func openPty() (master, slave *os.File, err error) {
	master, err = os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, err
	}
	var unlock int32
	var n uint32
	if err = ioctl(master, syscall.TIOCSPTLCK, unsafe.Pointer(&unlock)); err == nil {
		err = ioctl(master, syscall.TIOCGPTN, unsafe.Pointer(&n))
	}
	if err == nil {
		slave, err = os.OpenFile("/dev/pts/"+strconv.Itoa(int(n)), os.O_RDWR|syscall.O_NOCTTY, 0)
	}
	if err != nil {
		master.Close()
		return nil, nil, err
	}
	var tio syscall.Termios
	if err = ioctl(slave, syscall.TCGETS, unsafe.Pointer(&tio)); err == nil {
		tio.Oflag &^= syscall.OPOST
		err = ioctl(slave, syscall.TCSETS, unsafe.Pointer(&tio))
	}
	if err == nil {
		ws := [4]uint16{ptyRows, ptyCols} // struct winsize: rows, columns, and pixels nobody uses
		err = ioctl(slave, syscall.TIOCSWINSZ, unsafe.Pointer(&ws))
	}
	if err != nil {
		master.Close()
		slave.Close()
		return nil, nil, err
	}
	return master, slave, nil
}

func ioctl(f *os.File, req uintptr, arg unsafe.Pointer) error {
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), req, uintptr(arg)); e != 0 {
		return e
	}
	return nil
}
