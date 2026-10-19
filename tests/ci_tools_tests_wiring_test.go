package tests

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// El job `python` del workflow recoge los tests de herramienta
// (`tools/*/test_*.py`) por glob en vez de por lista fija. Esa decisión es la que
// impide el verde sin ancla: un tool test entra en la colección en el mismo commit
// que lo trae. El riesgo real no es que alguien liste mal los ficheros de hoy, es
// que el step deje de recogerlos — con la lista fija de antes, o con un
// `|| true` que tape el fallo. Por eso estos tests ejecutan el bloque `run:`
// extraído del YAML y miran comportamiento: qué rutas recibe pytest y qué exit
// code sale, sin depender de que pytest esté instalado aquí.

const workflowPath = "../.github/workflows/ci.yml"

// stepScript devuelve el bloque `run:` del único step que invoca los tests de
// seguridad. Si ese step desaparece o se duplica, el guard falla: es el ancla.
func stepScript(t *testing.T) string {
	t.Helper()

	raw, err := os.ReadFile(workflowPath)
	require.NoError(t, err, "no puedo leer %s", workflowPath)

	var wf struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string `yaml:"name"`
				Run  string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &wf), "workflow no parseable")

	var scripts []string
	for _, def := range wf.Jobs {
		for _, step := range def.Steps {
			if strings.Contains(step.Run, "test_security_mitigations.py") {
				scripts = append(scripts, step.Run)
			}
		}
	}
	require.Len(t, scripts, 1, "tiene que haber exactamente un step que lance los tests de seguridad y herramientas")

	return scripts[0]
}

// sandbox arma un árbol de mentira con las rutas indicadas (ficheros vacíos: el
// `python` del PATH es un doble, así que nunca se ejecutan de verdad).
func sandbox(t *testing.T, files ...string) string {
	t.Helper()

	dir := t.TempDir()
	for _, f := range files {
		full := filepath.Join(dir, filepath.FromSlash(f))
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte("pass\n"), 0o644))
	}
	return dir
}

// shimPython pone en el PATH un `python` que solo apunta los args y sale
// con el código que le digan: mide la colección sin arrastrar pytest.
func shimPython(t *testing.T, exitCode int) (binDir, argvFile string) {
	t.Helper()

	binDir = t.TempDir()
	argvFile = filepath.Join(t.TempDir(), "argv.txt")

	shim := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$SHIM_ARGV\"\nexit \"$SHIM_EXIT\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "python"), []byte(shim), 0o755))

	t.Setenv("SHIM_ARGV", argvFile)
	t.Setenv("SHIM_EXIT", strconv.Itoa(exitCode))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	return binDir, argvFile
}

// runStep ejecuta el bloque `run:` con el mismo shell que el runner de GitHub.
func runStep(t *testing.T, script, dir string) (int, string) {
	t.Helper()

	cmd := exec.Command("bash", "-eo", "pipefail", "-c", script)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()

	if err == nil {
		return 0, string(out)
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), string(out)
	}
	require.NoError(t, err, "no puedo ejecutar el step")

	return -1, string(out)
}

func recordedArgs(t *testing.T, argvFile string) []string {
	t.Helper()

	raw, err := os.ReadFile(argvFile)
	require.NoError(t, err, "el doble de python no se ejecutó")
	require.NotEmpty(t, raw, "el doble de python no recibió args")

	args := []string{}
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		if line != "" {
			args = append(args, line)
		}
	}
	return args
}

// fileArgs filtra los args que parecen rutas de fichero (todo lo que no es
// flag ni el nombre del módulo).
func fileArgs(args []string) []string {
	out := []string{}
	skipNext := false
	for _, a := range args {
		if skipNext {
			skipNext = false
			continue
		}
		switch a {
		case "-m":
			skipNext = true
		case "-v", "pytest":
		default:
			if !strings.HasPrefix(a, "-") {
				out = append(out, a)
			}
		}
	}
	sort.Strings(out)
	return out
}

// El glob recoge cualquier `tools/<dir>/test_*.py`, no una lista de nombres.
func TestCIStepCollectsToolTestsByGlob(t *testing.T) {
	dir := sandbox(t,
		"tests/test_security_mitigations.py",
		"tools/zztool/test_probe.py",
		"tools/another-one/test_second.py",
		"tools/data_analysis/test_main.py",
	)
	_, argvFile := shimPython(t, 0)

	code, out := runStep(t, stepScript(t), dir)
	require.Equal(t, 0, code, "el step tiene que pasar con el doble de python:\n%s", out)

	require.Equal(t, []string{
		"tests/test_security_mitigations.py",
		"tools/another-one/test_second.py",
		"tools/data_analysis/test_main.py",
		"tools/zztool/test_probe.py",
	}, fileArgs(recordedArgs(t, argvFile)))
}

// Con el glob sin expandir (ningún tool test en el árbol) el step no puede romper
// por `-e` ni pasar un `tools/*/test_*.py` literal a pytest.
func TestCIStepToleratesEmptyToolTests(t *testing.T) {
	dir := sandbox(t, "tests/test_security_mitigations.py", "tools/data_analysis/main.py")
	_, argvFile := shimPython(t, 0)

	code, out := runStep(t, stepScript(t), dir)
	require.Equal(t, 0, code, "sin tool tests el step pasa igual:\n%s", out)

	args := fileArgs(recordedArgs(t, argvFile))
	require.Equal(t, []string{"tests/test_security_mitigations.py"}, args)
	for _, a := range args {
		require.NotContains(t, a, "*", "el glob no expandido no debe llegar a pytest: %v", args)
	}
}

// Solo `test_*.py`: el patrón no se ensancha a `tools/*/*.py`.
func TestCIStepIgnoresNonTestFiles(t *testing.T) {
	dir := sandbox(t,
		"tests/test_security_mitigations.py",
		"tools/zztool/main.py",
		"tools/zztool/conftest.py",
		"tools/zztool/tool.yaml",
		"tools/zztool/testdata/sample.py",
		"tools/zztool/helper_test.py.bak",
	)
	_, argvFile := shimPython(t, 0)

	code, out := runStep(t, stepScript(t), dir)
	require.Equal(t, 0, code, "el step pasa:\n%s", out)

	require.Equal(t, []string{"tests/test_security_mitigations.py"}, fileArgs(recordedArgs(t, argvFile)))
}

// El fallo de un tool test tiene que tumbar el step. Un `|| true`, un `set +e` o un
// `continue` en el bucle lo taparían y el ancla dejaría de existir.
func TestCIStepPropagatesToolTestFailure(t *testing.T) {
	dir := sandbox(t,
		"tests/test_security_mitigations.py",
		"tools/zztool/test_probe.py",
	)
	_, argvFile := shimPython(t, 1)

	code, out := runStep(t, stepScript(t), dir)
	require.NotEqual(t, 0, code, "un test que falla tiene que tumbar el step, no salir 0:\n%s", out)
	require.Equal(t, []string{
		"tests/test_security_mitigations.py",
		"tools/zztool/test_probe.py",
	}, fileArgs(recordedArgs(t, argvFile)), "y el fichero que falla tiene que estar entre los recogidos")
}

// Guard de clase: un tool test que aparece en el árbol tiene que llegar a pytest.
// Se siembra uno sintético para que la comprobación no sea vacua mientras no haya
// ninguno real, y se añaden todos los `tools/*/test_*.py` que existan hoy: cuando
// un PR traiga el primero, este test ya lo está mirando.
func TestEveryToolTestIsCollected(t *testing.T) {
	script := stepScript(t)

	const synthetic = "tools/zz_synthetic_guard/test_probe.py"
	files := []string{"tests/test_security_mitigations.py", synthetic}

	matches, err := filepath.Glob("../tools/*/test_*.py")
	require.NoError(t, err)
	for _, m := range matches {
		rel, err := filepath.Rel("..", m)
		require.NoError(t, err)
		files = append(files, filepath.ToSlash(rel))
	}
	t.Logf("tool tests reales en el árbol: %d", len(matches))

	dir := sandbox(t, files...)
	_, argvFile := shimPython(t, 0)

	code, out := runStep(t, script, dir)
	require.Equal(t, 0, code, "el step pasa con los ficheros reales:\n%s", out)

	got := fileArgs(recordedArgs(t, argvFile))
	want := append([]string{}, files...)
	sort.Strings(want)

	require.Equal(t, want, got, "todos los tools/*/test_*.py del árbol tienen que llegar a pytest")
}
