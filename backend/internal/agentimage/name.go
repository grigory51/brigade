package agentimage

import (
	"errors"
	"regexp"
	"strings"
)

var imageNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,39}$`)
var imageOwnerPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func imageRef(userID, name string) (string, error) {
	if !imageNamePattern.MatchString(name) {
		return "", errors.New("название образа: до 40 символов a-z, 0-9, точки, дефисы и подчёркивания; первый символ — буква или цифра")
	}
	if !imageOwnerPattern.MatchString(userID) {
		return "", errors.New("ID пользователя нельзя использовать в теге образа")
	}
	return "brigade-build:" + userID + "." + name, nil
}

// DisplayName скрывает пользовательский namespace, сохраняя legacy refs без изменений.
func DisplayName(ref string) string {
	if tail, ok := strings.CutPrefix(ref, "brigade-build:"); ok {
		owner, name, ok := strings.Cut(tail, ".")
		if ok && imageOwnerPattern.MatchString(owner) && imageNamePattern.MatchString(name) {
			return name
		}
	}
	return ref
}
