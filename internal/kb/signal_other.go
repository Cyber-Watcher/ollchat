//go:build !unix

package kb

import "os"

// На системах без сигналов проверить процесс нечем: считаем замок живым,
// пока его не уберут вручную.
var sigZero os.Signal = nil
