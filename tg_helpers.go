package main

import (
	"regexp"
	"strconv"
)

var (
	publicGroupRX = regexp.MustCompile(`^-100`)
)

func makePublicGroupString(groupID int64) string {
	// https://gist.github.com/nafiesl/4ad622f344cd1dc3bb1ecbe468ff9f8a
	// groudid has leeading -100
	return publicGroupRX.ReplaceAllString(strconv.FormatInt(groupID, 10), "")
}
