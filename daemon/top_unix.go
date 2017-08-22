//+build !windows

package daemon

import (
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/docker/docker/api/types/container"
)

func validatePSArgs(psArgs string) error {
	// NOTE: \\s does not detect unicode whitespaces.
	// So we use fieldsASCII instead of strings.Fields in parsePSOutput.
	// See https://github.com/docker/docker/pull/24358
	// nolint: gosimple
	re := regexp.MustCompile("\\s+([^\\s]*)=\\s*(PID[^\\s]*)")
	for _, group := range re.FindAllStringSubmatch(psArgs, -1) {
		if len(group) >= 3 {
			k := group[1]
			v := group[2]
			if k != "pid" {
				return fmt.Errorf("specifying \"%s=%s\" is not allowed", k, v)
			}
		}
	}
	return nil
}

// fieldsASCII is similar to strings.Fields but only allows ASCII whitespaces
func fieldsASCII(s string) []string {
	fn := func(r rune) bool {
		switch r {
		case '\t', '\n', '\f', '\r', ' ':
			return true
		}
		return false
	}
	return strings.FieldsFunc(s, fn)
}

func appendProcess2ProcList(procList *container.ContainerTopOKBody, fields []string) {
	// Make sure number of fields equals number of header titles
	// merging "overhanging" fields
	process := fields[:len(procList.Titles)-1]
	process = append(process, strings.Join(fields[len(procList.Titles)-1:], " "))
	procList.Processes = append(procList.Processes, process)
}

func hasPid(pids []int, pid int) bool {
	for _, i := range pids {
		if i == pid {
			return true
		}
	}
	return false
}

func insertCharacterToLine(base_string string, index int, value string) string {
	new_string := base_string[:index] + value + base_string[index:]
	return new_string
}

func correctPidValue(line string, pidValue string) string {
	// PIDs are numeric values.  If pidValue contains any non-numeric characters
	// it's assumed that the value belongs in the ps column after PID
	var alpha_pos = -1
	for pos, character := range pidValue {
		if unicode.IsLetter(character) {
			alpha_pos = pos
			break
		}
	}

	if alpha_pos == -1 {
		// pidValue contains only numeric characters, unable to correct
		// the field.
		return line
	}

	pidValue_index := strings.Index(line, pidValue)
	split_at_index := alpha_pos + pidValue_index
	newline := insertCharacterToLine(line, split_at_index, " ")

	return newline
}

func parsePSOutput(output []byte, pids []int) (*container.ContainerTopOKBody, error) {
	procList := &container.ContainerTopOKBody{}

	lines := strings.Split(string(output), "\n")
	procList.Titles = fieldsASCII(lines[0])

	pidIndex := -1
	for i, name := range procList.Titles {
		if name == "PID" {
			pidIndex = i
		}
	}
	if pidIndex == -1 {
		return nil, fmt.Errorf("Couldn't find PID field in ps output")
	}

	// loop through the output and extract the PID from each line
	// fixing #30580, be able to display thread line also when "m" option used
	// in "docker top" client command
	preContainedPidFlag := false
	for _, line := range lines[1:] {
		if len(line) == 0 {
			continue
		}

		// Issue #34282 has identified a situation where the PID column
		// and the next column's fields are not separated by white space.
		// This is an attempt to correct the line when the column following PID
		// starts with a letter.
		line = correctPidValue(line, fieldsASCII(line)[pidIndex])

		fields := fieldsASCII(line)

		var (
			p   int
			err error
		)

		if fields[pidIndex] == "-" {
			if preContainedPidFlag {
				appendProcess2ProcList(procList, fields)
			}
			continue
		}
		p, err = strconv.Atoi(fields[pidIndex])
		if err != nil {
			return nil, fmt.Errorf("Unexpected pid '%s': %s", fields[pidIndex], err)
		}

		if hasPid(pids, p) {
			preContainedPidFlag = true
			appendProcess2ProcList(procList, fields)
			continue
		}
		preContainedPidFlag = false
	}
	return procList, nil
}

// ContainerTop lists the processes running inside of the given
// container by calling ps with the given args, or with the flags
// "-ef" if no args are given.  An error is returned if the container
// is not found, or is not running, or if there are any problems
// running ps, or parsing the output.
func (daemon *Daemon) ContainerTop(name string, psArgs string) (*container.ContainerTopOKBody, error) {
	if psArgs == "" {
		psArgs = "-ef"
	}

	if err := validatePSArgs(psArgs); err != nil {
		return nil, err
	}

	container, err := daemon.GetContainer(name)
	if err != nil {
		return nil, err
	}

	if !container.IsRunning() {
		return nil, errNotRunning(container.ID)
	}

	if container.IsRestarting() {
		return nil, errContainerIsRestarting(container.ID)
	}

	pids, err := daemon.containerd.GetPidsForContainer(container.ID)
	if err != nil {
		return nil, err
	}

	output, err := exec.Command("ps", strings.Split(psArgs, " ")...).Output()
	if err != nil {
		return nil, fmt.Errorf("Error running ps: %v", err)
	}
	procList, err := parsePSOutput(output, pids)
	if err != nil {
		return nil, err
	}
	daemon.LogContainerEvent(container, "top")
	return procList, nil
}
