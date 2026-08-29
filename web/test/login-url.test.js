import test from 'node:test';
import assert from 'node:assert/strict';

import { buildLoginURL } from '../src/helpers.js';

const appLocation = {
    protocol: 'https:',
    hostname: 'app.roost.example',
    port: '',
    origin: 'https://app.roost.example',
};

test('login redirect uses the server-advertised split login host', function() {
    assert.equal(
        buildLoginURL('https://login.roost.example', appLocation),
        'https://login.roost.example/login?next=https%3A%2F%2Fapp.roost.example%2F'
    );
});

test('login redirect keeps the historical app-prefix fallback for old relays', function() {
    assert.equal(
        buildLoginURL('', {
            protocol: 'https:', hostname: 'app.wingthing.ai', port: '',
            origin: 'https://app.wingthing.ai',
        }),
        'https://wingthing.ai/login?next=https%3A%2F%2Fapp.wingthing.ai%2F'
    );
});

test('login redirect rejects executable and credentialed advertised URLs', function() {
    for (const unsafe of ['javascript:alert(1)', 'https://user:secret@login.roost.example']) {
        assert.equal(
            buildLoginURL(unsafe, appLocation),
            'https://roost.example/login?next=https%3A%2F%2Fapp.roost.example%2F'
        );
    }
});
