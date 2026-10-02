#ifndef GOANIME_TRAILER_H
#define GOANIME_TRAILER_H
#include <stdint.h>
char *goanime_trailer_start(const char *youtube_id, double x, double y, double width, double height, uint64_t token);
char *goanime_trailer_move(uint64_t token, double x, double y, double width, double height);
char *goanime_trailer_command(uint64_t token, const char *command);
void goanime_trailer_stop(uint64_t token);
char *goanime_trailer_status(uint64_t token);
#endif
