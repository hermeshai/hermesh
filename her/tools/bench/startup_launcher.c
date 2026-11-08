/* Optional Linux benchmark clock: timestamp immediately before fork, including fork,
 * exec, the loader and the entire child lifetime, without Python's Popen preparation.
 * The child inherits the harness's cwd, environment and redirected stdio. No daemon.
 * cc -O2 -Wall -Wextra -Werror tools/bench/startup_launcher.c -o /tmp/startup-launcher
 */
#define _POSIX_C_SOURCE 200809L
#include <errno.h>
#include <signal.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <sys/wait.h>
#include <time.h>
#include <unistd.h>

static volatile sig_atomic_t child_pid;
static volatile sig_atomic_t timed_out;

static void expire(int signal_number) {
    (void)signal_number;
    timed_out = 1;
    if (child_pid > 0) kill(child_pid, SIGKILL);
}

static int64_t now_ns(void) {
    struct timespec ts;
    if (clock_gettime(CLOCK_MONOTONIC, &ts) != 0) {
        perror("clock_gettime");
        exit(125);
    }
    return (int64_t)ts.tv_sec * 1000000000 + ts.tv_nsec;
}

int main(int argc, char **argv) {
    if (argc < 3) {
        fprintf(stderr, "usage: startup-launcher TIMEOUT_SECONDS /absolute/command [args...]\n");
        return 125;
    }
    char *end;
    long timeout = strtol(argv[1], &end, 10);
    if (*end || timeout < 1 || timeout > 86400) return 125;
    struct sigaction action = {0};
    action.sa_handler = expire;
    sigemptyset(&action.sa_mask);
    if (sigaction(SIGALRM, &action, NULL) != 0) return 125;
    int64_t start = now_ns();
    pid_t pid = fork();
    if (pid < 0) { perror("fork"); return 125; }
    if (pid == 0) {
        execv(argv[2], &argv[2]);
        perror("execv");
        _exit(127);
    }
    child_pid = pid;
    alarm((unsigned int)timeout);
    int status;
    while (waitpid(pid, &status, 0) < 0) {
        if (errno != EINTR) { perror("waitpid"); return 125; }
    }
    int64_t finish = now_ns();
    alarm(0);
    int code = timed_out ? 124 : WIFEXITED(status) ? WEXITSTATUS(status) : 128 + WTERMSIG(status);
    fprintf(stderr, "\nHER_STARTUP_CLOCK {\"start_ns\":%lld,\"end_ns\":%lld}\n",
            (long long)start, (long long)finish);
    return code;
}
