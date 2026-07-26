#ifndef ICLOUD_REMINDERS_EVENTKIT_BRIDGE_H
#define ICLOUD_REMINDERS_EVENTKIT_BRIDGE_H

#include <stdlib.h>

typedef struct reminders_eventkit_store reminders_eventkit_store;

reminders_eventkit_store *reminders_eventkit_open(char **error_out);
void reminders_eventkit_close(reminders_eventkit_store *store);
char *reminders_eventkit_status(reminders_eventkit_store *store, char **error_out);
char *reminders_eventkit_lists(reminders_eventkit_store *store, char **error_out);
char *reminders_eventkit_show(reminders_eventkit_store *store, const char *input_json, char **error_out);
char *reminders_eventkit_add(reminders_eventkit_store *store, const char *input_json, char **error_out);
char *reminders_eventkit_complete(reminders_eventkit_store *store, const char *identifier, char **error_out);
int reminders_eventkit_delete(reminders_eventkit_store *store, const char *identifier, char **error_out);

#endif
