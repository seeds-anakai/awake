package main

/*
#cgo  CFLAGS: -x         objective-c
#cgo LDFLAGS: -framework IOKit
#cgo LDFLAGS: -framework CoreFoundation
#cgo LDFLAGS: -framework Foundation

#include <IOKit/ps/IOPowerSources.h>
#include <IOKit/ps/IOPSKeys.h>
#include <IOKit/pwr_mgt/IOPMLib.h>
#include <CoreFoundation/CoreFoundation.h>
#import <Foundation/Foundation.h>

long getThermalState(void) {
	@autoreleasepool {
		NSProcessInfoThermalState state = [[NSProcessInfo processInfo] thermalState];
		return (long)state;
	}
}

// 戻り値: 成功なら1、失敗なら0。percentOut/onACOut に結果を書き込む
int getBatteryStatus(int *percentOut, int *onACOut) {
	CFTypeRef blob = IOPSCopyPowerSourcesInfo();
	if (blob == NULL) return 0;

	CFArrayRef sources = IOPSCopyPowerSourcesList(blob);
	if (sources == NULL || CFArrayGetCount(sources) == 0) {
		if (sources) CFRelease(sources);
		CFRelease(blob);
		return 0;
	}

	CFDictionaryRef desc = IOPSGetPowerSourceDescription(
		blob, CFArrayGetValueAtIndex(sources, 0));
	if (desc == NULL) {
		CFRelease(sources);
		CFRelease(blob);
		return 0;
	}

	int capacity = 0;
	CFNumberRef capacityRef = (CFNumberRef)CFDictionaryGetValue(
		desc, CFSTR(kIOPSCurrentCapacityKey));
	if (capacityRef) {
		CFNumberGetValue(capacityRef, kCFNumberIntType, &capacity);
	}

	int onAC = 0;
	CFStringRef powerState = (CFStringRef)CFDictionaryGetValue(
		desc, CFSTR(kIOPSPowerSourceStateKey));
	if (powerState &&
		CFStringCompare(powerState, CFSTR(kIOPSACPowerValue), 0) == kCFCompareEqualTo) {
		onAC = 1;
	}

	*percentOut = capacity;
	*onACOut = onAC;

	CFRelease(sources);
	CFRelease(blob);
	return 1;
}
*/
import "C"

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"time"
	"unsafe"
)

// ============================================================
// caffeinate 相当
// ============================================================

func preventIdleSleep(reason string) (C.IOPMAssertionID, error) {
	var assertionID C.IOPMAssertionID
	cReason := C.CString(reason)
	defer C.free(unsafe.Pointer(cReason))
	cfReason := C.CFStringCreateWithCString(
		C.kCFAllocatorDefault, cReason, C.kCFStringEncodingUTF8)
	defer C.CFRelease(C.CFTypeRef(cfReason))

	ret := C.IOPMAssertionCreateWithName(
		C.kIOPMAssertionTypePreventUserIdleSystemSleep,
		C.kIOPMAssertionLevelOn,
		cfReason,
		&assertionID,
	)
	if ret != C.kIOReturnSuccess {
		return 0, fmt.Errorf("IOPMAssertionCreateWithName failed: %d", ret)
	}
	return assertionID, nil
}

func releaseIdleSleep(assertionID C.IOPMAssertionID) {
	C.IOPMAssertionRelease(assertionID)
}

// ============================================================
// 発熱監視
// ============================================================

type ThermalState int

const (
	ThermalNominal ThermalState = iota
	ThermalFair
	ThermalSerious
	ThermalCritical
)

func getThermalState() ThermalState {
	return ThermalState(C.getThermalState())
}

func watchThermal(threshold ThermalState, sustainedFor time.Duration, onTrigger func()) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	var hotSince time.Time
	for range ticker.C {
		if getThermalState() >= threshold {
			if hotSince.IsZero() {
				hotSince = time.Now()
			} else if time.Since(hotSince) >= sustainedFor {
				onTrigger()
				return
			}
		} else {
			hotSince = time.Time{}
		}
	}
}

func parseTimeout(s string) (time.Duration, error) {
	// 数字だけなら秒として扱う（元記事の "3600" == "1h" 相当）
	if _, err := strconv.Atoi(s); err == nil {
		s = s + "s"
	}
	return time.ParseDuration(s)
}

func batteryStatus() (int, bool, error) {
	var percent, onAC C.int
	ok := C.getBatteryStatus(&percent, &onAC)
	if ok == 0 {
		return 0, false, fmt.Errorf("failed to get battery status")
	}
	return int(percent), onAC != 0, nil
}

// ============================================================
// root helper
// ============================================================

const helperScript = `
trap "" INT TERM HUP
pmset -a disablesleep 1
while read -r _; do :; done
pmset -a disablesleep 0
`

type RootHelper struct {
	cmd *exec.Cmd
	w   *os.File
}

func startRootHelper() (*RootHelper, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command("sudo", "bash", "-c", helperScript)
	cmd.Stdin = r
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		r.Close()
		w.Close()
		return nil, err
	}
	r.Close()
	return &RootHelper{cmd: cmd, w: w}, nil
}

func (h *RootHelper) Restore() {
	h.w.Close()
	h.cmd.Wait()
}

// ============================================================
// main
// ============================================================

func main() {
	var (
		timeoutStr string
		batteryPct int
		safeMode   bool
	)
	fs := flag.NewFlagSet("awake", flag.ExitOnError)
	fs.StringVar(&timeoutStr, "t", "", "auto-stop after duration (e.g. 1h30m, 3600)")
	fs.StringVar(&timeoutStr, "timeout", "", "same as -t")
	fs.IntVar(&batteryPct, "b", 0, "auto-stop when battery <= this percent (1-99)")
	fs.IntVar(&batteryPct, "battery", 0, "same as -b")
	fs.BoolVar(&safeMode, "s", false, "auto-stop after sustained high thermal pressure")
	fs.BoolVar(&safeMode, "safe", false, "same as -s")
	fs.Parse(os.Args[1:])

	var timeout time.Duration
	if timeoutStr != "" {
		var err error
		timeout, err = parseTimeout(timeoutStr)
		if err != nil {
			fmt.Fprintln(os.Stderr, "invalid -t value:", err)
			os.Exit(1)
		}
	}

	fmt.Println("awake: disabling sleep (sudo may ask for your password — once; exit never prompts)")

	helper, err := startRootHelper()
	if err != nil {
		fmt.Fprintln(os.Stderr, "failed to start root helper:", err)
		os.Exit(1)
	}

	assertionID, err := preventIdleSleep("awake: AI dev session")
	if err != nil {
		fmt.Fprintln(os.Stderr, "failed to prevent idle sleep:", err)
	}

	msg := fmt.Sprintf("awake: running (PID=%d", os.Getpid())
	if timeout > 0 {
		msg += fmt.Sprintf(", timeout=%s", timeout)
	}
	msg += "). Press Ctrl+C to release and exit."
	fmt.Println(msg)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	stopReason := make(chan string, 1)

	// -t
	var timeoutCh <-chan time.Time
	if timeout > 0 {
		timeoutCh = time.After(timeout)
	}

	// -b：60秒ごとにポーリングし、AC接続中はスキップ
	if batteryPct > 0 {
		go func() {
			ticker := time.NewTicker(60 * time.Second)
			defer ticker.Stop()
			for range ticker.C {
				pct, onAC, err := batteryStatus()
				if err != nil || onAC {
					continue // AC接続中はチェックしない
				}
				if pct <= batteryPct {
					stopReason <- fmt.Sprintf("battery at %d%% (threshold %d%%)", pct, batteryPct)
					return
				}
			}
		}()
	}

	// -s
	if safeMode {
		fmt.Println("awake: safe mode on (auto-stop after 3min of serious+ thermal pressure)")
		go watchThermal(ThermalSerious, 3*time.Minute, func() {
			stopReason <- "sustained thermal pressure"
		})
	}

	select {
	case <-sig:
		fmt.Println("\nawake: received signal, restoring sleep settings (root helper; no sudo prompt)")
	case <-timeoutCh:
		fmt.Println("\nawake: timeout reached, restoring sleep settings")
	case reason := <-stopReason:
		fmt.Printf("\nawake: auto-stop triggered (%s), restoring sleep settings\n", reason)
	}

	releaseIdleSleep(assertionID)
	helper.Restore()
	fmt.Println("awake: sleep settings restored (pmset -a disablesleep 0)")
}
