#include <seccomp.h>
#include <stdio.h>
#include <stdlib.h>
#include <unistd.h>
#include <errno.h>

int main(int argc, char *argv[])
{
    if (argc < 2) {
        printf("Usage: %s <program>\n", argv[0]);
        return 1;
    }

    scmp_filter_ctx filter = seccomp_init(SCMP_ACT_ALLOW);

    if (filter == NULL) {
        perror("seccomp_init");
        return 1;
    }
    seccomp_rule_add(
        filter,
        SCMP_ACT_ERRNO(EPERM),
        SCMP_SYS(uname),
        0
    );

    if (seccomp_load(filter) < 0) {
        perror("seccomp_load");
        return 1;
    }

    seccomp_release(filter);

    execvp(argv[1], &argv[1]);

    perror("execvp");
    return 1;
}