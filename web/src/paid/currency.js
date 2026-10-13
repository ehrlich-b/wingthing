export function parseUSD(value) {
    if (typeof value !== 'string' || !/^(?:0|[1-9]\d{0,2})(?:\.\d{1,2})?$/.test(value.trim())) throw new Error('Enter USD from 0.00 to 200.00 with at most 2 decimal places.');
    const [whole, fraction = ''] = value.trim().split('.');
    const cents = Number(whole) * 100 + Number(fraction.padEnd(2, '0'));
    if (cents > 20000) throw new Error('The cap cannot exceed $200.00 USD.');
    return cents;
}
