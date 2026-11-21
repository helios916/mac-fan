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
	keyWheelUp
	keyWheelDown
	keyClick
	keyRune
	keyEnter
	keyBackspace
	keyCancel
)

// inputEvent carries a key press or a mouse event. Mouse events arrive with
// 1-based terminal coordinates from SGR mode (1006).
type inputEvent struct {
	key key
	x   int
	y   int
	ch  byte
}

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

func inputLoop(ch chan<- inputEvent) {
	r := bufio.NewReader(os.Stdin)
	emit := func(k key) { ch <- inputEvent{key: k} }
	for {
		b, err := r.ReadByte()
		if err != nil {
			close(ch)
			return
		}
		switch b {
		case 0x03:
			emit(keyQuit)
		case 0x0d, 0x0a:
			emit(keyEnter)
		case 0x7f, 0x08:
			emit(keyBackspace)
		case ' ':
			emit(keyToggle)
		case 'q', 'Q':
			emit(keyCancel)
		case 'k', 'K':
			emit(keyUp)
		case 'j', 'J':
			emit(keyDown)
		case '+', '=':
			emit(keyPlus)
		case '-', '_':
			emit(keyMinus)
		case 'm', 'M':
			emit(keyManual)
		case 'a', 'A':
			emit(keyAuto)
		case 'r', 'R':
			emit(keyAllAuto)
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
			// SGR mouse reports look like ESC [ < Cb ; Cx ; Cy M|m.
			if b3 == '<' {
				if ev, ok := readSGRMouse(r); ok {
					ch <- ev
				}
				continue
			}
			switch b3 {
			case 'A':
				emit(keyUp)
			case 'B':
				emit(keyDown)
			case '5', '6':
				if b4, err := r.ReadByte(); err == nil && b4 == '~' {
					if b3 == '5' {
						emit(keyPgUp)
					} else {
						emit(keyPgDn)
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
		default:
			if b >= '0' && b <= '9' {
				ch <- inputEvent{key: keyRune, ch: b}
			}
		}
	}
}

// readSGRMouse parses the numeric part of an SGR mouse report and turns it
// into a wheel or click event. Releases and drags are ignored.
func readSGRMouse(r *bufio.Reader) (inputEvent, bool) {
	params := make([]int, 3)
	idx := 0
	for {
		c, err := r.ReadByte()
		if err != nil {
			return inputEvent{}, false
		}
		switch {
		case c >= '0' && c <= '9':
			params[idx] = params[idx]*10 + int(c-'0')
		case c == ';':
			if idx == 2 {
				return inputEvent{}, false
			}
			idx++
		default:
			if c != 'M' {
				return inputEvent{}, false
			}
		}
		if c == 'M' {
			break
		}
	}
	cb, x, y := params[0], params[1], params[2]
	switch cb {
	case 64:
		return inputEvent{key: keyWheelUp}, true
	case 65:
		return inputEvent{key: keyWheelDown}, true
	case 0:
		return inputEvent{key: keyClick, x: x, y: y}, true
	}
	return inputEvent{}, false
}
