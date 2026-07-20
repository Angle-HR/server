ALTER TABLE waitlist.waitlist
    DROP CONSTRAINT waitlist_region_check;

ALTER TABLE waitlist.waitlist
    ADD CONSTRAINT waitlist_region_check
        CHECK (region IN ('uk', 'us', 'africa', 'eu', 'asia'));
