package container

import (
	"fmt"
	"os/user"
	"strconv"
)

type HostUser struct {
	Username string
	UID      int
	GID      int
	HomeDir  string
}

func DetectHostUser() HostUser {
	u, err := user.Current()
	if err != nil {
		return HostUser{Username: "node", UID: 1001, GID: 1001, HomeDir: "/home/node"}
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	return HostUser{
		Username: u.Username,
		UID:      uid,
		GID:      gid,
		HomeDir:  u.HomeDir,
	}
}

func (h HostUser) ContainerHome() string {
	return fmt.Sprintf("/home/%s", h.Username)
}
