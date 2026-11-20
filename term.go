package main

import (
	"bufio"
	"os"
	"syscall"
	"unsafe"
)

type key int

const (
	keyNone key = iota
	keyUp
	keyDown
	keyPgUp
	keyPgDn
	keyPlus
	keyMinus
	keyManual
	keyAuto
	keyAllAuto
	keyToggle
	keyQuit
)

type winsize struct {
	Row    uint16
	Col    uint16
	Xpixel uint16
	Ypixel uint16
}

func ioctl(fd int, req uintptr, arg unsafe.Pointer) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), req, uintptr(arg))
	if errno != 0 {
		return errno
	}
	return nil
}

func termSize() (rows, cols int) {
	ws := winsize{}
	if err := ioctl(1, syscall.TIOCGWINSZ, unsafe.Pointer(&ws)); err == nil && ws.Col > 0 {
		return int(ws.Row), int(ws.Col)
	}
	return 24, 80
}

func makeRaw(fd int) (*syscall.Termios, error) {
	old := &syscall.Termios{}
	if err := ioctl(fd, syscall.TIOCGETA, unsafe.Pointer(old)); err != nil {
		return nil, err
	}
	raw := *old
	raw.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK | syscall.ISTRIP |
		syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON
	raw.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
	raw.Cflag &^= syscall.CSIZE | syscall.PARENB
	raw.Cflag |= syscall.CS8
	raw.Cc[syscall.VMIN] = 1
	raw.Cc[syscall.VTIME] = 0
	if err := ioctl(fd, syscall.TIOCSETA, unsafe.Pointer(&raw)); err != nil {
		return nil, err
	}
	return old, nil
}

func restoreTermios(fd int, old *syscall.Termios) {
	if old == nil {
		return
	}
	_ = ioctl(fd, syscall.TIOCSETA, unsafe.Pointer(old))
}

func inputLoop(ch chan<- key) {
	r := bufio.NewReader(os.Stdin)
	for {
		b, err := r.ReadByte()
		if err != nil {
			close(ch)
			return
		}
		switch b {
		case 0x03:
			ch <- keyQuit
		case ' ':
			ch <- keyToggle
		case 'q', 'Q':
			ch <- keyQuit
		case 'k', 'K':
			ch <- keyUp
		case 'j', 'J':
			ch <- keyDown
		case '+', '=':
			ch <- keyPlus
		case '-', '_':
			ch <- keyMinus
		case 'm', 'M':
			ch <- keyManual
		case 'a', 'A':
			ch <- keyAuto
		case 'r', 'R':
			ch <- keyAllAuto
		case 0x1b:
			b2, err := r.ReadByte()
			if err != nil {
				close(ch)
				return
			}
			if b2 != '[' {
				continue
			}
			b3, err := r.ReadByte()
			if err != nil {
				close(ch)
				return
			}
			switch b3 {
			case 'A':
				ch <- keyUp
			case 'B':
				ch <- keyDown
			case '5', '6':
				if b4, err := r.ReadByte(); err == nil && b4 == '~' {
					if b3 == '5' {
						ch <- keyPgUp
					} else {
						ch <- keyPgDn
					}
				}
			default:
				if b3 < 0x40 || b3 > 0x7e {
					for {
						c, err := r.ReadByte()
						if err != nil {
							close(ch)
							return
						}
						if c >= 0x40 && c <= 0x7e {
							break
						}
					}
				}
			}
		}
	}
}
