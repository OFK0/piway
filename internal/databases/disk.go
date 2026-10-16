package databases

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// OnSDCard reports whether path is on an SD card (an mmcblk device), by
// following its filesystem's device to the disk in sysfs. When that
// cannot be told, it says yes: the careful choice for card life.
func OnSDCard(path string) bool {
	disk, err := diskOf(path, "/sys")
	if err != nil {
		return true
	}
	return strings.HasPrefix(disk, "mmcblk")
}

// diskOf returns the block device path is on, such as mmcblk0p2 or sda1.
func diskOf(path, sys string) (string, error) {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return "", err
	}
	dev := uint64(st.Dev)
	link, err := os.Readlink(filepath.Join(sys, "dev", "block", fmt.Sprintf("%d:%d", unix.Major(dev), unix.Minor(dev))))
	if err != nil {
		return "", err
	}
	return filepath.Base(link), nil
}
