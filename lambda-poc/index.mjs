import puppeteer from 'puppeteer-core';
import chromium from '@sparticuz/chromium-min';

const DEFAULT_ASIN = 'B0D1V7ZQ9Y';
const CHROMIUM_PACK_URL = 'https://github.com/Sparticuz/chromium/releases/download/v149.0.0/chromium-v149.0.0-pack.x64.tar';
const AMAZON_URL = 'https://www.amazon.co.jp/dp/';
const PAGE_TIMEOUT_MS = 30000;

const getTargetUrl = (event) => {
    const asin = event?.asin || DEFAULT_ASIN;
    return `${AMAZON_URL}${asin}`;
};

const extractProductInfo = async (page, url) => {
    return page.evaluate((pageUrl) => {
        const title = document.querySelector('#productTitle')?.textContent?.trim() || null;
        const price = document.querySelector('#kindle-price, #tmm-grid-swatch-KINDLE .slot-price')?.textContent?.trim() || null;
        const paperPrice = document.querySelector("[id^='tmm-grid-swatch']:not([id$='KINDLE']) .slot-price")?.textContent?.trim() || null;
        const points = document.querySelector('#tmm-grid-swatch-KINDLE .slot-buyingPoints, #tmm-grid-swatch-OTHER .slot-buyingPoints')?.textContent?.trim() || null;
        const coupon = document.querySelector('i.a-icon.a-icon-addon.newCouponBadge')?.textContent?.trim() || null;
        const blocked = document.body?.textContent?.includes('画像に表示されている文字を入力') || false;

        return {
            title,
            price,
            paperPrice,
            points,
            coupon,
            blocked,
            url: pageUrl,
            fetchedAt: new Date().toISOString()
        };
    }, url);
};

const launchBrowser = async () => {
    chromium.setGraphicsMode = false;
    return puppeteer.launch({
        args: chromium.args,
        defaultViewport: chromium.defaultViewport,
        executablePath: await chromium.executablePath(CHROMIUM_PACK_URL),
        headless: true
    });
};

const handler = async (event) => {
    const url = getTargetUrl(event);
    const startedAt = Date.now();
    const browser = await launchBrowser();

    try {
        const page = await browser.newPage();
        await page.goto(url, {
            waitUntil: 'domcontentloaded',
            timeout: PAGE_TIMEOUT_MS
        });

        const result = await extractProductInfo(page, url);
        return {
            ok: true,
            status: await page.evaluate(() => document.readyState),
            durationMs: Date.now() - startedAt,
            ...result
        };
    } finally {
        await browser.close();
    }
};

export { handler };
