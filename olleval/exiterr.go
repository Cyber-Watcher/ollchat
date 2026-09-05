package main

import (
	"errors"
	"os/exec"
)

// asExitError отделяет «команда отработала и вернула ненулевой код» от
// «команду не удалось запустить». Разница важная: первое — результат проверки,
// второе — наша поломка, и балл за неё ставить нельзя.
func asExitError(err error, target **exec.ExitError) bool {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		*target = ee
		return true
	}
	return false
}
