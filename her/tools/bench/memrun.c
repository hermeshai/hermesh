/* fork + exec argv[1..], then print the child's peak RSS from wait4 (KiB on Linux). The child
 * inherits stdio. Being tiny, this process adds almost nothing to the pre-exec high-water mark:
 * on Linux ru_maxrss survives exec, so a child forked directly from Python reports Python's RSS.
 * cc -O2 tools/bench/memrun.c -o target/memrun */
#define _GNU_SOURCE
#include <stdio.h>
#include <stdlib.h>
#include <sys/resource.h>
#include <sys/wait.h>
#include <unistd.h>
int main(int argc, char **argv) {
    if (argc < 2) return 125;
    pid_t pid = fork();
    if (pid < 0) return 125;
    if (pid == 0) { execv(argv[1], &argv[1]); _exit(127); }
    int status; struct rusage ru;
    if (wait4(pid, &status, 0, &ru) < 0) return 125;
    fprintf(stderr, "\nMEMRUN {\"max_rss_kib\":%ld}\n", ru.ru_maxrss);
    return WIFEXITED(status) ? WEXITSTATUS(status) : 128 + WTERMSIG(status);
}
