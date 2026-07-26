#import <EventKit/EventKit.h>
#import <Foundation/Foundation.h>
#include "eventkit_bridge.h"

struct reminders_eventkit_store { EKEventStore *value; };
static const int64_t EventKitTimeoutSeconds = 60;

static char *copy_string(NSString *value) {
    if (value == nil) return NULL;
    return strdup([value UTF8String]);
}

static void set_error(char **out, NSString *message) {
    if (out != NULL) *out = copy_string(message ?: @"unknown EventKit error");
}

static char *json_result(id value, char **error_out) {
    NSError *error = nil;
    NSData *data = [NSJSONSerialization dataWithJSONObject:value options:0 error:&error];
    if (data == nil) {
        set_error(error_out, [NSString stringWithFormat:@"encode EventKit response: %@", error.localizedDescription]);
        return NULL;
    }
    NSString *json = [[[NSString alloc] initWithData:data encoding:NSUTF8StringEncoding] autorelease];
    return copy_string(json);
}

static NSDictionary *decode_input(const char *input, char **error_out) {
    NSData *data = [NSData dataWithBytes:input length:strlen(input)];
    NSError *error = nil;
    id value = [NSJSONSerialization JSONObjectWithData:data options:0 error:&error];
    if (![value isKindOfClass:[NSDictionary class]]) {
        set_error(error_out, [NSString stringWithFormat:@"decode EventKit input: %@", error.localizedDescription ?: @"expected object"]);
        return nil;
    }
    return value;
}

static NSString *authorization_name(EKAuthorizationStatus status) {
    switch (status) {
        case EKAuthorizationStatusNotDetermined: return @"not_determined";
        case EKAuthorizationStatusRestricted: return @"restricted";
        case EKAuthorizationStatusDenied: return @"denied";
        case EKAuthorizationStatusFullAccess: return @"full_access";
#ifdef __MAC_14_0
        case EKAuthorizationStatusWriteOnly: return @"write_only";
#endif
    }
    return @"unknown";
}

static BOOL ensure_access(EKEventStore *store, char **error_out) {
    const char *forced = getenv("REMINDERS_EVENTKIT_TEST_AUTH");
    if (forced != NULL && strcmp(forced, "denied") == 0) {
        set_error(error_out, @"Reminders access denied by REMINDERS_EVENTKIT_TEST_AUTH negative control");
        return NO;
    }
    if (forced != NULL && strcmp(forced, "timeout") == 0) {
        set_error(error_out, @"EventKit authorization timed out by REMINDERS_EVENTKIT_TEST_AUTH negative control");
        return NO;
    }
    EKAuthorizationStatus status = [EKEventStore authorizationStatusForEntityType:EKEntityTypeReminder];
    if (status == EKAuthorizationStatusFullAccess) return YES;
    if (status != EKAuthorizationStatusNotDetermined) {
        set_error(error_out, [NSString stringWithFormat:@"Reminders access %@; enable Full Access in System Settings > Privacy & Security > Reminders", authorization_name(status)]);
        return NO;
    }
    const char *no_prompt = getenv("REMINDERS_EVENTKIT_NO_PROMPT");
    if (no_prompt != NULL && strcmp(no_prompt, "1") == 0) {
        set_error(error_out, @"Reminders access not_determined for this background binary; authorize the installed reminders binary interactively, then restart the LaunchAgent");
        return NO;
    }
    dispatch_semaphore_t semaphore = dispatch_semaphore_create(0);
    __block BOOL granted = NO;
    __block NSError *requestError = nil;
    if (@available(macOS 14.0, *)) {
        [store requestFullAccessToRemindersWithCompletion:^(BOOL didGrant, NSError *error) {
            granted = didGrant;
            requestError = [error retain];
            dispatch_semaphore_signal(semaphore);
        }];
    } else {
#pragma clang diagnostic push
#pragma clang diagnostic ignored "-Wdeprecated-declarations"
        [store requestAccessToEntityType:EKEntityTypeReminder completion:^(BOOL didGrant, NSError *error) {
            granted = didGrant;
            requestError = [error retain];
            dispatch_semaphore_signal(semaphore);
        }];
#pragma clang diagnostic pop
    }
    long waited = dispatch_semaphore_wait(semaphore, dispatch_time(DISPATCH_TIME_NOW, EventKitTimeoutSeconds * NSEC_PER_SEC));
#if !OS_OBJECT_USE_OBJC
    dispatch_release(semaphore);
#endif
    if (waited != 0) {
        [requestError release];
        set_error(error_out, [NSString stringWithFormat:@"EventKit authorization timed out after %llds", EventKitTimeoutSeconds]);
        return NO;
    }
    if (!granted) {
        NSString *detail = requestError.localizedDescription ?: @"Full Access was not granted";
        set_error(error_out, [NSString stringWithFormat:@"Reminders access denied: %@", detail]);
        [requestError release];
        return NO;
    }
    [requestError release];
    return YES;
}

static EKCalendar *find_calendar(EKEventStore *store, NSString *needle, char **error_out) {
    NSArray<EKCalendar *> *calendars = [store calendarsForEntityType:EKEntityTypeReminder];
    NSMutableArray<EKCalendar *> *matches = [NSMutableArray array];
    for (EKCalendar *calendar in calendars) {
        if ([calendar.calendarIdentifier caseInsensitiveCompare:needle] == NSOrderedSame ||
            [calendar.title caseInsensitiveCompare:needle] == NSOrderedSame) {
            [matches addObject:calendar];
        }
    }
    if (matches.count == 1) return matches[0];
    if (matches.count == 0) set_error(error_out, [NSString stringWithFormat:@"reminder list '%@' not found", needle]);
    else set_error(error_out, [NSString stringWithFormat:@"reminder list '%@' is ambiguous", needle]);
    return nil;
}

static NSString *iso_date(NSDate *date) {
    if (date == nil) return nil;
    NSISO8601DateFormatter *formatter = [[[NSISO8601DateFormatter alloc] init] autorelease];
    formatter.formatOptions = NSISO8601DateFormatWithInternetDateTime | NSISO8601DateFormatWithFractionalSeconds;
    return [formatter stringFromDate:date];
}

static NSDictionary *reminder_dict(EKReminder *reminder) {
    NSMutableDictionary *value = [NSMutableDictionary dictionary];
    value[@"id"] = reminder.calendarItemIdentifier ?: @"";
    value[@"title"] = reminder.title ?: @"";
    value[@"completed"] = @(reminder.completed);
    value[@"priority"] = @(reminder.priority);
    value[@"list_name"] = reminder.calendar.title ?: @"";
    value[@"list_ref"] = reminder.calendar.calendarIdentifier ?: @"";
    if (reminder.notes.length > 0) value[@"notes"] = reminder.notes;
    if (reminder.URL != nil) value[@"url"] = reminder.URL.absoluteString;
    if (reminder.completionDate != nil) value[@"completion_date"] = iso_date(reminder.completionDate);
    if (reminder.lastModifiedDate != nil) value[@"modified_ts"] = @((long long)reminder.lastModifiedDate.timeIntervalSince1970);
    if (reminder.dueDateComponents != nil) {
        NSCalendar *calendar = reminder.dueDateComponents.calendar ?: [NSCalendar currentCalendar];
        NSDate *date = [calendar dateFromComponents:reminder.dueDateComponents];
        if (date != nil) value[@"due"] = iso_date(date);
    }
    return value;
}

static NSArray<EKReminder *> *fetch_reminders(EKEventStore *store, EKCalendar *calendar, char **error_out) {
    NSPredicate *predicate = [store predicateForRemindersInCalendars:@[calendar]];
    dispatch_semaphore_t semaphore = dispatch_semaphore_create(0);
    __block NSArray<EKReminder *> *result = nil;
    [store fetchRemindersMatchingPredicate:predicate completion:^(NSArray<EKReminder *> *reminders) {
        result = [reminders retain];
        dispatch_semaphore_signal(semaphore);
    }];
    long waited = dispatch_semaphore_wait(semaphore, dispatch_time(DISPATCH_TIME_NOW, EventKitTimeoutSeconds * NSEC_PER_SEC));
#if !OS_OBJECT_USE_OBJC
    dispatch_release(semaphore);
#endif
    if (waited != 0) {
        [result release];
        set_error(error_out, [NSString stringWithFormat:@"EventKit reminder fetch timed out after %llds", EventKitTimeoutSeconds]);
        return nil;
    }
    return [result autorelease];
}

static NSDateComponents *parse_due(NSString *input, char **error_out) {
    if (input.length == 0) return nil;
    NSDate *date = nil;
    NSISO8601DateFormatter *iso = [[[NSISO8601DateFormatter alloc] init] autorelease];
    date = [iso dateFromString:input];
    BOOL dateOnly = NO;
    if (date == nil) {
        NSDateFormatter *day = [[[NSDateFormatter alloc] init] autorelease];
        day.locale = [NSLocale localeWithLocaleIdentifier:@"en_US_POSIX"];
        day.dateFormat = @"yyyy-MM-dd";
        date = [day dateFromString:input];
        dateOnly = date != nil;
    }
    if (date == nil) {
        set_error(error_out, @"due must be ISO-8601 or YYYY-MM-DD");
        return nil;
    }
    NSCalendar *calendar = [NSCalendar currentCalendar];
    NSCalendarUnit units = NSCalendarUnitYear | NSCalendarUnitMonth | NSCalendarUnitDay;
    if (!dateOnly) units |= NSCalendarUnitHour | NSCalendarUnitMinute | NSCalendarUnitSecond | NSCalendarUnitTimeZone;
    NSDateComponents *components = [calendar components:units fromDate:date];
    components.calendar = calendar;
    return components;
}

reminders_eventkit_store *reminders_eventkit_open(char **error_out) {
    @autoreleasepool {
        reminders_eventkit_store *store = calloc(1, sizeof(reminders_eventkit_store));
        if (store == NULL) { set_error(error_out, @"allocate EventKit store"); return NULL; }
        store->value = [[EKEventStore alloc] init];
        return store;
    }
}

void reminders_eventkit_close(reminders_eventkit_store *store) {
    if (store == NULL) return;
    [store->value release];
    free(store);
}

char *reminders_eventkit_status(reminders_eventkit_store *store, char **error_out) {
    @autoreleasepool {
        if (!ensure_access(store->value, error_out)) return NULL;
        return json_result(@{@"authenticated": @YES, @"backend": @"eventkit/in-process", @"detail": @"Full access"}, error_out);
    }
}

char *reminders_eventkit_lists(reminders_eventkit_store *store, char **error_out) {
    @autoreleasepool {
        if (!ensure_access(store->value, error_out)) return NULL;
        NSMutableArray *values = [NSMutableArray array];
        for (EKCalendar *calendar in [store->value calendarsForEntityType:EKEntityTypeReminder]) {
            [values addObject:@{@"id": calendar.calendarIdentifier ?: @"", @"name": calendar.title ?: @""}];
        }
        return json_result(values, error_out);
    }
}

char *reminders_eventkit_show(reminders_eventkit_store *store, const char *input_json, char **error_out) {
    @autoreleasepool {
        if (!ensure_access(store->value, error_out)) return NULL;
        NSDictionary *input = decode_input(input_json, error_out); if (input == nil) return NULL;
        EKCalendar *calendar = find_calendar(store->value, input[@"list"], error_out); if (calendar == nil) return NULL;
        NSArray<EKReminder *> *reminders = fetch_reminders(store->value, calendar, error_out); if (reminders == nil) return NULL;
        BOOL includeCompleted = [input[@"include_completed"] boolValue];
        NSMutableArray *values = [NSMutableArray array];
        for (EKReminder *reminder in reminders) if (includeCompleted || !reminder.completed) [values addObject:reminder_dict(reminder)];
        return json_result(values, error_out);
    }
}

char *reminders_eventkit_add(reminders_eventkit_store *store, const char *input_json, char **error_out) {
    @autoreleasepool {
        if (!ensure_access(store->value, error_out)) return NULL;
        NSDictionary *input = decode_input(input_json, error_out); if (input == nil) return NULL;
        NSString *title = input[@"title"], *list = input[@"list"];
        if (title.length == 0 || list.length == 0) { set_error(error_out, @"title and list are required"); return NULL; }
        EKCalendar *calendar = find_calendar(store->value, list, error_out); if (calendar == nil) return NULL;
        EKReminder *reminder = [EKReminder reminderWithEventStore:store->value];
        reminder.title = title; reminder.calendar = calendar;
        if ([input[@"notes"] length] > 0) reminder.notes = input[@"notes"];
        NSString *priority = input[@"priority"];
        if ([priority isEqualToString:@"high"]) reminder.priority = 1;
        else if ([priority isEqualToString:@"medium"]) reminder.priority = 5;
        else if ([priority isEqualToString:@"low"]) reminder.priority = 9;
        else if (priority.length > 0 && ![priority isEqualToString:@"none"]) { set_error(error_out, @"priority must be none, low, medium, or high"); return NULL; }
        if ([input[@"due"] length] > 0) { reminder.dueDateComponents = parse_due(input[@"due"], error_out); if (reminder.dueDateComponents == nil) return NULL; }
        NSError *error = nil;
        if (![store->value saveReminder:reminder commit:YES error:&error]) { set_error(error_out, [NSString stringWithFormat:@"save reminder: %@", error.localizedDescription]); return NULL; }
        return json_result(reminder_dict(reminder), error_out);
    }
}

char *reminders_eventkit_complete(reminders_eventkit_store *store, const char *identifier, char **error_out) {
    @autoreleasepool {
        if (!ensure_access(store->value, error_out)) return NULL;
        NSString *needle = [NSString stringWithUTF8String:identifier];
        EKCalendarItem *item = [store->value calendarItemWithIdentifier:needle];
        if (![item isKindOfClass:[EKReminder class]]) { set_error(error_out, [NSString stringWithFormat:@"reminder '%@' not found", needle]); return NULL; }
        EKReminder *reminder = (EKReminder *)item;
        reminder.completed = YES;
        NSError *error = nil;
        if (![store->value saveReminder:reminder commit:YES error:&error]) { set_error(error_out, [NSString stringWithFormat:@"complete reminder: %@", error.localizedDescription]); return NULL; }
        return json_result(reminder_dict(reminder), error_out);
    }
}

int reminders_eventkit_delete(reminders_eventkit_store *store, const char *identifier, char **error_out) {
    @autoreleasepool {
        if (!ensure_access(store->value, error_out)) return 0;
        NSString *needle = [NSString stringWithUTF8String:identifier];
        EKCalendarItem *item = [store->value calendarItemWithIdentifier:needle];
        if (![item isKindOfClass:[EKReminder class]]) {
            set_error(error_out, [NSString stringWithFormat:@"reminder '%@' not found", needle]);
            return 0;
        }
        NSError *error = nil;
        if (![store->value removeReminder:(EKReminder *)item commit:YES error:&error]) {
            set_error(error_out, [NSString stringWithFormat:@"delete reminder: %@", error.localizedDescription]);
            return 0;
        }
        return 1;
    }
}
